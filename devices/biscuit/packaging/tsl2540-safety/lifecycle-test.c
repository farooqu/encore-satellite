/* SPDX-License-Identifier: GPL-2.0-or-later
 * Actual probe/remove + ALS translation units; modeled kernfs active-reference
 * draining, not a booted kernel or a model of sensor physics.
 */
#include "lifecycle-shim.h"
#include <linux/i2c/ams_tsl2540.h>
#include "ams_i2c.h"

#define CHECK(c, message) do { if (!(c)) { \
	fprintf(stderr, "FAIL: %s (line %d)\n", message, __LINE__); exit(1); \
} } while (0)

_Thread_local unsigned int lock_depth;
static pthread_mutex_t gate = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t changed = PTHREAD_COND_INITIALIZER;
static struct i2c_driver *driver;
static struct i2c_client client;
static struct device_node node;
static struct tsl2540_i2c_platform_data pdata;
static u8 registers[MAX_REGS];
static u64 clock_ns = 1000000000ULL;
static struct {
	const struct attribute_group *group;
	bool published;
	unsigned int active;
} groups[2];
static bool pause_reader, reader_entered, release_reader, draining;
static int create_count, fail_create, alloc_count, fail_input_alloc;
static int register_count, fail_register, inputs, allocations;
static bool irq, fail_irq;
static void *managed;

u64 ktime_get_ns(void) { return clock_ns++; }
void *kmalloc(size_t n, int flags) { void *p = malloc(n); CHECK(p, "allocation"); allocations++; return p; }
void *kzalloc(size_t n, int flags) { void *p = kmalloc(n, flags); memset(p, 0, n); return p; }
void *devm_kzalloc(struct device *d, size_t n, int flags)
{
	CHECK(!managed, "single managed platform allocation");
	return managed = kzalloc(n, flags);
}
void kfree(void *p)
{
	if (!p) return;
	for (unsigned int i = 0; i < ARRAY_SIZE(groups); i++)
		CHECK(!groups[i].published && !groups[i].active,
			"published sysfs groups survive driver teardown");
	CHECK(p != &pdata, "borrowed platform data must not be freed");
	allocations--;
	free(p);
}
struct input_dev *input_allocate_device(void)
{
	if (++alloc_count == fail_input_alloc) return NULL;
	inputs++;
	return kzalloc(sizeof(struct input_dev), 0);
}
void input_free_device(struct input_dev *d)
{
	CHECK(d && !d->registered, "free only unregistered input");
	inputs--;
	kfree(d);
}
int input_register_device(struct input_dev *d)
{
	if (++register_count == fail_register) return -EREMOTEIO;
	d->registered = true;
	return 0;
}
void input_unregister_device(struct input_dev *d)
{
	CHECK(d && d->registered, "unregister a live input exactly once");
	if (d->users && d->close) d->close(d);
	d->registered = false;
	input_free_device(d);
}
int sysfs_create_group(struct kobject *k, const struct attribute_group *g)
{
	CHECK(k->dev == &client.dev, "group belongs to client");
	if (++create_count == fail_create) return -ENOMEM;
	for (unsigned int i = 0; i < ARRAY_SIZE(groups); i++) {
		if (!groups[i].published && !groups[i].active) {
			groups[i].group = g;
			groups[i].published = true;
			return 0;
		}
	}
	CHECK(false, "group capacity");
	return -ENOMEM;
}
void sysfs_remove_group(struct kobject *k, const struct attribute_group *g)
{
	/* Pinned kernfs removal deactivates new calls and drains active calls.
	 * Waiting while chip->lock is held deadlocks a pending sysfs reader. */
	CHECK(!lock_depth, "must not hold chip mutex while draining sysfs");
	pthread_mutex_lock(&gate);
	for (unsigned int i = 0; i < ARRAY_SIZE(groups); i++) {
		if (groups[i].group != g || !groups[i].published) continue;
		groups[i].published = false;
		if (groups[i].active) {
			draining = true;
			pthread_cond_broadcast(&changed);
		}
		while (groups[i].active) pthread_cond_wait(&changed, &gate);
	}
	pthread_mutex_unlock(&gate);
}
int request_threaded_irq(int n, void *top, irqreturn_t (*fn)(int, void *), int flags, const char *name, void *p)
{
	if (fail_irq) return -EBUSY;
	CHECK(!irq, "single IRQ registration");
	irq = true;
	return 0;
}
void free_irq(int n, void *p) { CHECK(irq, "IRQ registered"); irq = false; }
int i2c_add_driver(struct i2c_driver *d) { driver = d; return 0; }
void i2c_del_driver(struct i2c_driver *d) { CHECK(d == driver, "driver unregister"); }
int ams_i2c_blk_read(struct i2c_client *c, u8 reg, u8 *v, int n)
{
	memcpy(v, registers + reg, n);
	return n;
}
int ams_i2c_read(struct i2c_client *c, u8 reg, u8 *v) { return ams_i2c_blk_read(c, reg, v, 1); }
int ams_i2c_write(struct i2c_client *c, u8 *sh, u8 reg, u8 v) { sh[reg] = registers[reg] = v; return 0; }
int ams_i2c_write_direct(struct i2c_client *c, u8 reg, u8 v) { registers[reg] = v; return 0; }
int ams_i2c_modify(struct i2c_client *c, u8 *sh, u8 reg, u8 mask, u8 v)
{
	return ams_i2c_write(c, sh, reg, (registers[reg] & ~mask) | v);
}
int ams_i2c_reg_blk_write(struct i2c_client *c, u8 reg, u8 *v, int n)
{
	memcpy(registers + reg, v, n);
	return 0;
}

#include "ams_tsl2540.c"

static ssize_t read_attribute(const char *name)
{
	char buf[PAGE_SIZE];
	struct device_attribute *attr = NULL;
	unsigned int slot = 0;
	pthread_mutex_lock(&gate);
	for (unsigned int i = 0; i < ARRAY_SIZE(groups); i++) {
		if (!groups[i].published) continue;
		for (struct attribute **a = groups[i].group->attrs; *a; a++) {
			if (!strcmp((*a)->name, name)) {
				attr = (struct device_attribute *)*a;
				slot = i;
			}
		}
	}
	if (!attr) { pthread_mutex_unlock(&gate); return -ENOENT; }
	groups[slot].active++;
	if (pause_reader) {
		reader_entered = true;
		pthread_cond_broadcast(&changed);
		while (!release_reader) pthread_cond_wait(&changed, &gate);
	}
	pthread_mutex_unlock(&gate);
	ssize_t rc = attr->show(&client.dev, attr, buf);
	pthread_mutex_lock(&gate);
	groups[slot].active--;
	pthread_cond_broadcast(&changed);
	pthread_mutex_unlock(&gate);
	return rc;
}
static void teardown(struct device *d)
{
	CHECK(!inputs && !irq, "input/IRQ callbacks stopped before platform teardown");
	for (unsigned int i = 0; i < ARRAY_SIZE(groups); i++)
		CHECK(!groups[i].published && !groups[i].active,
			"published sysfs groups survive driver teardown");
}
static void reset(bool dt)
{
	CHECK(!allocations && !inputs && !irq, "previous bind released resources");
	memset(&client, 0, sizeof(client));
	memset(&pdata, 0, sizeof(pdata));
	memset(groups, 0, sizeof(groups));
	memset(registers, 0, sizeof(registers));
	client.dev.kobj.dev = &client.dev;
	client.dev.of_node = &node;
	client.dev.platform_data = dt ? NULL : &pdata;
	pdata.als_name = "test-als";
	pdata.platform_teardown = teardown;
	pdata.parameters.als_time = 1;
	pdata.parameters.d_factor = 1;
	pdata.parameters.lux_segment[0].ch0_coef = 1000;
	registers[TSL2540_REG_ID] = 0xe4;
	registers[TSL2540_REG_AUXID] = 1;
	registers[TSL2540_REG_REVID] = 0x61;
	create_count = alloc_count = register_count = 0;
	fail_create = fail_input_alloc = fail_register = 0;
	fail_irq = pause_reader = reader_entered = release_reader = draining = false;
}
static void devres_release(void)
{
	if (managed) { kfree(managed); managed = NULL; }
	CHECK(!client.dev.data, "client data cleared");
	CHECK(!allocations && !inputs && !irq, "all acquired resources released");
	CHECK(read_attribute("als_measurement") == -ENOENT && read_attribute("regs") == -ENOENT,
		"removed interfaces unavailable");
}
static void *reader(void *name)
{
	ssize_t rc = read_attribute(name);
	CHECK(rc > 0, "active sysfs read completes before teardown");
	return NULL;
}
static void *remove_driver(void *unused)
{
	CHECK(!driver->remove(&client), "remove succeeds");
	return NULL;
}
static void concurrent_remove(const char *attribute)
{
	pthread_t r, remover;
	reset(false);
	CHECK(!driver->probe(&client, driver->id_table), "probe before concurrent read");
	struct tsl2540_chip *chip = client.dev.data;
	mutex_lock(&chip->lock);
	CHECK(!tsl2540_configure_als_mode(chip, 1), "enable test measurement");
	mutex_unlock(&chip->lock);
	CHECK(read_attribute("als_measurement") == -EAGAIN, "holdoff begins");
	clock_ns += 30000000000ULL;
	pause_reader = true;
	CHECK(!pthread_create(&r, NULL, reader, (void *)attribute), "start reader");
	pthread_mutex_lock(&gate);
	while (!reader_entered) pthread_cond_wait(&changed, &gate);
	pthread_mutex_unlock(&gate);
	CHECK(!pthread_create(&remover, NULL, remove_driver, NULL), "start unbind");
	pthread_mutex_lock(&gate);
	while (!draining) pthread_cond_wait(&changed, &gate);
	CHECK(client.dev.data == chip, "state remains alive while draining");
	release_reader = true;
	pthread_cond_broadcast(&changed);
	pthread_mutex_unlock(&gate);
	CHECK(read_attribute(attribute) == -ENOENT, "unbind rejects new reads of deactivated attribute");
	CHECK(!pthread_join(r, NULL) && !pthread_join(remover, NULL), "reader/unbind complete");
	devres_release();
}
int main(void)
{
	CHECK(!test_module_init(), "register actual driver");
	for (int dt = 0; dt <= 1; dt++) {
		reset(dt);
		CHECK(!driver->probe(&client, driver->id_table), "bind/rebind");
		CHECK(read_attribute("als_measurement") == -EAGAIN, "disabled ABI visible while bound");
#ifdef TSL2540_ENABLE_INPUT
		((struct tsl2540_chip *)client.dev.data)->a_idev->users = 1;
#endif
		CHECK(!driver->remove(&client), "unbind");
		devres_release();
	}
	concurrent_remove("als_measurement");
	concurrent_remove("regs");
	for (int dt = 0; dt <= 1; dt++) {
		for (int stage = 1; stage <= 7; stage++) {
			reset(dt);
			if (stage <= 2) fail_create = stage;
			else if (stage <= 4) fail_input_alloc = stage - 2;
			else if (stage <= 6) fail_register = stage - 4;
			else fail_irq = true;
#ifndef TSL2540_ENABLE_INPUT
			if (stage >= 3 && stage <= 6) continue;
#endif
#ifndef TSL2540_ENABLE_INTERRUPT
			if (stage == 7) continue;
#endif
			CHECK(driver->probe(&client, driver->id_table) < 0, "injected probe failure propagates");
			devres_release();
		}
	}
	test_module_exit();
	puts("PASS: actual probe/remove, concurrent ALS/raw reads, rebind, borrowed/DT ownership and probe unwind");
	return 0;
}
