/* SPDX-License-Identifier: GPL-2.0-or-later */
#include "kernel-shim.h"
#include <stdatomic.h>
#include <linux/i2c/ams_tsl2540.h>

_Thread_local unsigned int lock_depth;
static struct tsl2540_chip chip;
static struct tsl2540_i2c_platform_data pdata;
static struct i2c_client client;
static u8 registers[MAX_REGS];
static u64 now_ns = 1000000000ULL;
static int fail_reg = -1, fail_result, data_result = 4;
static int write_failure = -1, writes, read_count;
static _Thread_local bool reject_writes;
static atomic_int writer_started;
static bool start_writer_on_channels;

u64 ktime_get_ns(void) { return now_ns++; }

int ams_i2c_blk_read(struct i2c_client *c, u8 reg, u8 *v, int n)
{
	int result = reg == fail_reg ? fail_result :
		(reg == TSL2540_REG_CH0DATA ? data_result : n);
	assert(lock_depth == 1);
	read_count++;
	if (reg == TSL2540_REG_CH0DATA && start_writer_on_channels) {
		atomic_store(&writer_started, 1);
		/* The writer must wait for this entire sysfs operation to unlock. */
		while (atomic_load(&writer_started) != 2) { }
	}
	if (result >= 0) memcpy(v, registers + reg, min(result, n));
	return result;
}
int ams_i2c_read(struct i2c_client *c, u8 reg, u8 *v)
{
	return ams_i2c_blk_read(c, reg, v, 1);
}
int ams_i2c_write(struct i2c_client *c, u8 *shadow, u8 reg, u8 v)
{
	assert(lock_depth == 1);
	assert(!reject_writes);
	writes++;
	/* Match the vendor helper: shadow changes even on a failed write. */
	shadow[reg] = v;
	if (writes == write_failure) return -EREMOTEIO;
	registers[reg] = v;
	return 0;
}
int ams_i2c_modify(struct i2c_client *c, u8 *sh, u8 reg, u8 mask, u8 v)
{
	u8 old = registers[reg];
	return ams_i2c_write(c, sh, reg, (old & ~mask) | v);
}
int ams_i2c_reg_blk_write(struct i2c_client *c, u8 reg, u8 *v, int n)
{
	assert(lock_depth == 1 && !reject_writes);
	writes++;
	memcpy(registers + reg, v, n);
	return 0;
}

/* Include, do not reimplement or extract, the actual patched source. */
#include "ams_tsl2540_als.c"

#define CHECK(c, message) do { if (!(c)) { \
	fprintf(stderr, "FAIL: %s (line %d)\n", message, __LINE__); exit(1); \
} } while (0)

static ssize_t show(struct device_attribute *a, char *out)
{
	ssize_t rc;
	memset(out, '!', PAGE_SIZE);
	reject_writes = true;
	rc = a->show(&client.dev, a, out);
	reject_writes = false;
	CHECK(!lock_depth, "sysfs read released mutex");
	if (rc < 0) CHECK(out[0] == '!', "failed read emitted no old record");
	return rc;
}

static ssize_t store(struct device_attribute *a, const char *value)
{
	ssize_t rc = a->store(&client.dev, a, value, strlen(value));
	CHECK(!lock_depth, "sysfs write released mutex");
	return rc;
}

static void channels(u16 ch0, u16 ch1)
{
	registers[TSL2540_REG_CH0DATA] = ch0;
	registers[TSL2540_REG_CH0DATA_HI] = ch0 >> 8;
	registers[TSL2540_REG_CH1DATA] = ch1;
	registers[TSL2540_REG_CH1DATA_HI] = ch1 >> 8;
}

static void ready(void)
{
#ifndef BASELINE
	char out[PAGE_SIZE];
	CHECK(show(&dev_attr_als_lux, out) == -EAGAIN, "first observation deferred");
	now_ns += 30000000000ULL;
#endif
}

static void setup(void)
{
	memset(&chip, 0, sizeof(chip));
	memset(&pdata, 0, sizeof(pdata));
	memset(registers, 0, sizeof(registers));
	CHECK(!pthread_mutex_init(&chip.lock.native, NULL), "mutex initialized");
	client.dev.data = &chip;
	chip.client = &client;
	chip.pdata = &pdata;
	pdata.lux400_lux = 400;
	chip.params.als_time = 1;
	chip.params.d_factor = 1;
	chip.params.lux_segment[0].ch0_coef = 1000;
	chip.params.lux_segment[0].ch1_coef = 260;
	chip.params.lux_segment[1].ch0_coef = 800;
	chip.params.lux_segment[1].ch1_coef = 270;
#ifndef BASELINE
	chip.measurement_initialized = true; /* Model successful probe completion. */
	CHECK(store(&dev_attr_als_power_state, "1") == 1, "enable via sysfs");
#else
	/* Isolate the old read-error bug from the separately known enable lock bug. */
	mutex_lock(&chip.lock);
	CHECK(dev_attr_als_power_state.store(&client.dev, &dev_attr_als_power_state,
		"1", 1) == 1, "enable via old sysfs");
	mutex_unlock(&chip.lock);
#endif
	channels(12345, 6789);
	ready();
}

static void regression(void)
{
	char out[PAGE_SIZE];
	channels(1234, 678);
	CHECK(show(&dev_attr_als_lux, out) > 0 && !strcmp(out, "375\n"),
		"integer estimated lux uses asymmetric channels without rescaling");
	data_result = -EREMOTEIO;
	CHECK(show(&dev_attr_als_lux, out) == -EREMOTEIO,
		"failed bus read must not return cached lux");
	for (data_result = 0; data_result < 4; data_result++)
		CHECK(show(&dev_attr_als_lux, out) == -EIO, "short read fails closed");
	channels(0, 0);
	CHECK(show(&dev_attr_als_lux, out) > 0 && !strcmp(out, "0\n"),
		"recovery to zero lux is valid");
}

#ifndef BASELINE
static void record_test(void)
{
	char out[PAGE_SIZE];
	unsigned int version, lux, ch0, ch1;
	unsigned long long seq, start, end, last = 0;
	int n;
	channels(12345, 6789);
	for (int i = 0; i < 2; i++) {
		CHECK(show(&dev_attr_als_measurement, out) > 0, "complete record");
		CHECK(sscanf(out, "%u %llu %llu %llu %u %u %u%n", &version,
			&seq, &start, &end, &lux, &ch0, &ch1, &n) == 7 &&
			!strcmp(out + n, "\n"), "versioned seven-field record");
		CHECK(version == 1 && seq > last && start < end && end <= now_ns &&
			lux == 3757 && ch0 == 12345 && ch1 == 6789,
			"coherent metadata and repeated equal values get new acquisition sequences");
		last = seq;
	}
	chip.measurement_sequence = UINT64_MAX;
	CHECK(show(&dev_attr_als_measurement, out) > 0 && !strncmp(out, "1 0 ", 4),
		"sequence wraps modulo 2^64");
	last = chip.measurement_sequence;
	data_result = -ETIMEDOUT;
	CHECK(show(&dev_attr_als_measurement, out) == -ETIMEDOUT &&
		chip.measurement_sequence == last, "failed record never advances sequence");
	data_result = 4;
}

static void state_test(void)
{
	char out[PAGE_SIZE];
	chip.measurement_initialized = false;
	CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN, "uninitialized refused");
	chip.measurement_initialized = true;
	chip.in_suspend = 1;
	CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN, "suspend refused");
	chip.in_suspend = 0;
	chip.unpowered = true;
	CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN, "unpowered refused");
	chip.unpowered = false;
	CHECK(store(&dev_attr_als_power_state, "0") == 1, "disable");
	CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN, "disabled refused");
	CHECK(store(&dev_attr_als_power_state, "1") == 1, "reenable");
	ready();
	CHECK(store(&dev_attr_als_auto_gain, "1") == 1, "auto mode enabled");
	CHECK(show(&dev_attr_als_measurement, out) == -EBUSY, "auto mode refused without writes");
	CHECK(show(&dev_attr_als_lux, out) == -EBUSY, "legacy lux is also read-only");
	CHECK(show(&dev_attr_als_ch0, out) > 0 && show(&dev_attr_als_ch1, out) > 0,
		"legacy cached channel reads cannot trigger autogain writes");
	CHECK(store(&dev_attr_als_auto_gain, "0") == 1, "manual mode restored");
	ready();
	registers[TSL2540_REG_CFG3] = 0x10;
	CHECK(show(&dev_attr_als_measurement, out) == -EOPNOTSUPP, "sleep-after-interrupt refused");
	registers[TSL2540_REG_CFG3] = 0;
	for (int i = 0; i < 8; i++) {
		const int regs[] = {0x80, 0x81, 0x83, 0x8d, 0x90, 0x9f, 0xd6, 0xab};
		fail_reg = regs[i];
		fail_result = -ENXIO;
		CHECK(show(&dev_attr_als_measurement, out) == -ENXIO, "config read error propagated");
		fail_result = 0;
		CHECK(show(&dev_attr_als_measurement, out) == -EIO, "short config read refused");
	}
	fail_reg = -1;
	registers[TSL2540_REG_CFG1] ^= 1;
	CHECK(show(&dev_attr_als_measurement, out) == -EIO, "actual config must match conversion state");
	registers[TSL2540_REG_CFG1] ^= 1;
	ready();
	CHECK(show(&dev_attr_als_measurement, out) > 0, "config recovery");
}

static void arithmetic_test(void)
{
	char out[PAGE_SIZE];
	CHECK(store(&dev_attr_als_itime, "0") == 1, "set zero time");
	CHECK(show(&dev_attr_als_lux, out) == -ERANGE, "zero CPL cannot divide");
	CHECK(store(&dev_attr_als_itime, "3") == 1, "restore one integration step");
	ready();
	CHECK(store(&dev_attr_als_lux_table, "0,1000,260,800,270") > 0, "zero factor set");
	CHECK(show(&dev_attr_als_lux, out) == -ERANGE, "zero factor refused");
	CHECK(store(&dev_attr_als_lux_table, "2816,1000,260,800,270") > 0, "low nonzero CPL");
	channels(65535, 0);
	CHECK(show(&dev_attr_als_lux, out) > 0 && !strcmp(out, "65535\n"), "wide arithmetic clamps high lux");
	CHECK(store(&dev_attr_als_lux_table, "1,1000,260,800,270") > 0, "restore factor");
	channels(65535, 0);
	CHECK(show(&dev_attr_als_lux, out) > 0 && !strcmp(out, "23272\n"), "32-bit numerator overflow regression");
	channels(100, 65535);
	CHECK(show(&dev_attr_als_lux, out) > 0 && !strcmp(out, "0\n"), "negative estimate clamps to zero");
	CHECK(store(&dev_attr_als_gain, "64") > 0, "set high gain");
	CHECK(store(&dev_attr_als_itime, "718") > 0, "set high integration");
	CHECK(show(&dev_attr_als_lux, out) == -ERANGE, "CPL overflow is rejected not wrapped");
	CHECK(store(&dev_attr_als_gain, "1") > 0, "restore gain");
	CHECK(store(&dev_attr_als_itime, "3") > 0, "restore time");
	ready();
	pdata.lux0_ch0 = -1;
	CHECK(show(&dev_attr_als_lux, out) == -ERANGE, "invalid negative baseline refused");
	pdata.lux0_ch0 = 0;
	pdata.lux400_lux = 0;
	CHECK(show(&dev_attr_als_calibrated_lux, out) == -ERANGE, "legacy calibration zero guard");
	pdata.lux400_lux = 400;
}

static void control_failure_test(void)
{
	char out[PAGE_SIZE];
	for (int i = 1; i <= 8; i++) {
		write_failure = writes + i;
		CHECK(store(&dev_attr_als_power_state, "1") == -EREMOTEIO, "enable failure propagated");
		CHECK(show(&dev_attr_als_lux, out) == -EAGAIN, "failed enable cannot publish");
		write_failure = -1;
		CHECK(store(&dev_attr_als_power_state, "1") == 1, "enable recovery");
	}
	ready();
	/* Three gain-change writes followed by the seven-register flush. */
	for (int i = 1; i <= 10; i++) {
		write_failure = writes + i;
		CHECK(store(&dev_attr_als_gain, "4") == -EREMOTEIO, "gain failure propagated");
		CHECK(show(&dev_attr_als_lux, out) == -EAGAIN, "failed gain cannot publish");
		write_failure = -1;
		CHECK(store(&dev_attr_als_gain, "4") == 1, "gain retry completes");
		CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN,
			"gain retry cannot silently reenable measurement");
		now_ns += 30000000000ULL;
		CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN,
			"gain retry remains disabled beyond maximum settling holdoff");
		CHECK(store(&dev_attr_als_power_state, "1") == 1, "gain error recovery requires explicit enable");
	}
	CHECK(store(&dev_attr_als_gain, "1") == 1, "restore gain");
	struct device_attribute *setters[] = {
		&dev_attr_als_itime, &dev_attr_als_wtime,
		&dev_attr_als_az_iterations, &dev_attr_als_persist,
	};
	const char *values[] = {"6", "100", "64", "2"};
	for (unsigned int s = 0; s < ARRAY_SIZE(setters); s++) {
		for (int i = 1; i <= 7; i++) {
			write_failure = writes + i;
			CHECK(store(setters[s], values[s]) == -EREMOTEIO,
				"configuration flush failure propagated");
			CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN,
				"partial configuration failure disables measurement");
			write_failure = -1;
			CHECK(store(setters[s], values[s]) > 0, "setter retry completes");
			CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN,
				"successful setter retry cannot silently reenable measurement");
			now_ns += 30000000000ULL;
			CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN,
				"setter retry remains disabled beyond maximum settling holdoff");
			CHECK(store(&dev_attr_als_power_state, "1") == 1, "explicit recovery");
			ready();
			CHECK(show(&dev_attr_als_measurement, out) > 0,
				"explicit enable and holdoff restore measurement");
		}
	}
	CHECK(store(&dev_attr_als_itime, "3") == 1, "restore integration");
	CHECK(store(&dev_attr_als_wtime, "0") == 1, "restore wait");
	ready();
}

static void tail_flush_regression(void)
{
	char out[PAGE_SIZE];
	write_failure = writes + 4;
	CHECK(store(&dev_attr_als_gain, "4") == -EREMOTEIO,
		"gain tail flush must propagate its bus error");
	CHECK(show(&dev_attr_als_measurement, out) == -EAGAIN,
		"gain tail flush failure disables measurement");
}

static void *change_time(void *unused)
{
	while (!atomic_load(&writer_started)) { }
	atomic_store(&writer_started, 2);
	CHECK(store(&dev_attr_als_itime, "6") == 1, "concurrent integration change");
	return NULL;
}

static void settling_test(void)
{
	char out[PAGE_SIZE];
	u64 observed;
	CHECK(store(&dev_attr_als_wtime, "100") == 3, "set 35-step wait time");
	observed = now_ns;
	CHECK(show(&dev_attr_als_lux, out) == -EAGAIN, "configuration observation is discarded");
	/* 2 cycles × (2 integration + 36 wait steps) × 2.90ms = 220.4ms. */
	now_ns = observed + 220400000ULL - 1;
	CHECK(show(&dev_attr_als_lux, out) == -EAGAIN, "one nanosecond before holdoff ends");
	now_ns = observed + 220400000ULL;
	CHECK(show(&dev_attr_als_lux, out) > 0, "holdoff equality permits bus acquisition");
	CHECK(store(&dev_attr_als_wtime, "0") == 1, "restore wait time");
	ready();
}

static void locking_test(void)
{
	char out[PAGE_SIZE];
	pthread_t writer;
	channels(12345, 6789);
	start_writer_on_channels = true;
	CHECK(!pthread_create(&writer, NULL, change_time, NULL), "start concurrent writer");
	CHECK(show(&dev_attr_als_lux, out) > 0 && !strcmp(out, "3757\n"),
		"sample uses old CPL through conversion despite pending writer");
	CHECK(!pthread_join(writer, NULL), "join writer");
	start_writer_on_channels = false;
	CHECK(show(&dev_attr_als_lux, out) == -EAGAIN, "configuration writer invalidates settling");
	now_ns += 30000000000ULL;
	CHECK(show(&dev_attr_als_lux, out) > 0 && !strcmp(out, "1878\n"),
		"next sample uses new CPL after settling");
}
#endif

int main(int argc, char **argv)
{
#ifdef HOST32
	CHECK(sizeof(long) == 4 && sizeof(void *) == 4, "32-bit host arithmetic model");
#endif
	setup();
#ifndef BASELINE
	if (argc == 2 && !strcmp(argv[1], "flush-regression")) {
		tail_flush_regression();
		puts("PASS: gain tail-flush regression");
		return 0;
	}
#endif
	regression();
#ifndef BASELINE
	record_test();
	state_test();
	arithmetic_test();
	control_failure_test();
	settling_test();
	locking_test();
#endif
	CHECK(read_count > 0, "exercised bus acquisition");
	puts("PASS: actual-source measurement ABI, errors, arithmetic, controls, settling and locking");
	return 0;
}
