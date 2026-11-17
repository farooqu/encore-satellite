/* SPDX-License-Identifier: GPL-2.0-or-later
 * Host-only kernel boundary for executing the actual vendor ALS translation unit.
 * This models locking, sysfs and SMBus calls, not a TSL2540 or a running kernel.
 */
#ifndef TSL2540_TEST_KERNEL_H
#define TSL2540_TEST_KERNEL_H
#include <assert.h>
#include <errno.h>
#include <inttypes.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>

typedef uint8_t u8;
typedef uint16_t u16;
typedef uint32_t u32;
typedef uint64_t u64;
typedef int32_t s32;
typedef int64_t s64;
typedef uint16_t __le16;
#define cpu_to_le16(x) (x)
#define le16_to_cpu(x) (x)
#define ARRAY_SIZE(x) (sizeof(x) / sizeof((x)[0]))
#define min(a,b) ({ __typeof__(a) _a=(a); __typeof__(b) _b=(b); _a<_b?_a:_b; })
#define max(a,b) ({ __typeof__(a) _a=(a); __typeof__(b) _b=(b); _a>_b?_a:_b; })
#define min_t(t,a,b) min((t)(a),(t)(b))
#define max_t(t,a,b) max((t)(a),(t)(b))
#define div_u64(a,b) ((u64)(a)/(u32)(b))
#define div64_s64(a,b) ((s64)(a)/(s64)(b))
#define PAGE_SIZE 4096
#define S_IRUGO 0444
#define S_IWUSR 0200
#define dev_info(...) ((void)0)
#define dev_err(...) ((void)0)
#define pr_info(...) ((void)0)

extern _Thread_local unsigned int lock_depth;
struct mutex { pthread_mutex_t native; };
static inline void mutex_lock(struct mutex *m)
{
	assert(!lock_depth);
	assert(!pthread_mutex_lock(&m->native));
	lock_depth++;
}
static inline void mutex_unlock(struct mutex *m)
{
	assert(lock_depth == 1);
	lock_depth--;
	assert(!pthread_mutex_unlock(&m->native));
}
struct device;
struct kobject { struct device *dev; };
struct device {
	void *data;
	void *platform_data;
	struct device_node *of_node;
	struct kobject kobj;
};
struct i2c_client { struct device dev; int irq; void *adapter; };
struct input_dev {
	struct device dev;
	const char *name;
	struct { int bustype; } id;
	unsigned long evbit[1], absbit[1];
	int (*open)(struct input_dev *);
	void (*close)(struct input_dev *);
	int users;
	bool registered;
};
struct attribute { const char *name; unsigned int mode; };
struct attribute_group { struct attribute **attrs; };
struct device_attribute {
	struct attribute attr;
	ssize_t (*show)(struct device *, struct device_attribute *, char *);
	ssize_t (*store)(struct device *, struct device_attribute *, const char *, size_t);
};
#define DEVICE_ATTR(n,m,r,w) struct device_attribute dev_attr_##n = {{#n,m},r,w}
static inline void *dev_get_drvdata(struct device *d) { return d->data; }
#define ABS_MISC 0
#define input_report_abs(...) ((void)0)
#define input_sync(...) ((void)0)
static inline int kstrtoul(const char *s, unsigned int base, unsigned long *v)
{
	char *end;
	errno = 0;
	*v = strtoul(s, &end, base);
	return errno || end == s ? -EINVAL : 0;
}
static inline int strtobool(const char *s, bool *v)
{
	if (*s != '0' && *s != '1') return -EINVAL;
	*v = *s == '1';
	return 0;
}
extern u64 ktime_get_ns(void);
#endif
