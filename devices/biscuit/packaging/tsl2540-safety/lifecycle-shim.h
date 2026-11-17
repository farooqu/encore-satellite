/* SPDX-License-Identifier: GPL-2.0-or-later */
#ifndef TSL2540_LIFECYCLE_SHIM_H
#define TSL2540_LIFECYCLE_SHIM_H
#include "kernel-shim.h"
#define GFP_KERNEL 0
#define __init
#define __exit
#define MODULE_DEVICE_TABLE(...)
#define MODULE_DESCRIPTION(...)
#define MODULE_LICENSE(...)
#define module_init(fn) int test_module_init(void) { return fn(); }
#define module_exit(fn) void test_module_exit(void) { fn(); }
#define pr_err(...) ((void)0)
#define pr_warn(...) ((void)0)
#define mdelay(...) ((void)0)
#define BUS_I2C 0
#define EV_ABS 0
#define ABS_DISTANCE 1
#define set_bit(...) ((void)0)
#define input_set_abs_params(...) ((void)0)
#define I2C_FUNC_SMBUS_BYTE_DATA 1
#define IRQF_TRIGGER_FALLING 1
#define IRQF_SHARED 2
#define IRQF_ONESHOT 4
#define IRQ_HANDLED 1
#define IRQ_NONE 0
typedef int irqreturn_t;
struct device_node { int unused; };
struct of_device_id { const char *compatible; };
struct i2c_device_id { const char *name; unsigned long data; };
struct i2c_driver {
	struct { const char *name; void *pm; } driver;
	struct i2c_device_id *id_table;
	int (*probe)(struct i2c_client *, const struct i2c_device_id *);
	int (*remove)(struct i2c_client *);
};
static inline void mutex_init(struct mutex *m) { assert(!pthread_mutex_init(&m->native, NULL)); }
static inline void dev_set_drvdata(struct device *d, void *p) { d->data = p; }
static inline void i2c_set_clientdata(struct i2c_client *c, void *p) { c->dev.data = p; }
static inline void *i2c_get_clientdata(struct i2c_client *c) { return c->dev.data; }
static inline const char *dev_name(struct device *d) { return "test-tsl2540"; }
static inline bool i2c_check_functionality(void *a, int f) { return true; }
static inline int device_create_file(struct device *d, struct device_attribute *a) { return 0; }
static inline void device_remove_file(struct device *d, struct device_attribute *a) { }
static inline const void *of_match_device(const struct of_device_id *ids, struct device *d) { return ids; }
static inline int of_property_read_string(struct device_node *n, const char *p, const char **v)
{
	if (strcmp(p, "als_name")) return -EINVAL;
	*v = "test-als";
	return 0;
}
static inline int of_property_read_u32(struct device_node *n, const char *p, u32 *v) { return -EINVAL; }
static inline bool of_property_read_bool(struct device_node *n, const char *p) { return false; }
static inline struct device_node *of_find_node_by_path(const char *p) { return NULL; }
static inline void *of_get_property(struct device_node *n, const char *p, void *len) { return NULL; }
void *kmalloc(size_t, int);
void *kzalloc(size_t, int);
void *devm_kzalloc(struct device *, size_t, int);
void kfree(void *);
struct input_dev *input_allocate_device(void);
void input_free_device(struct input_dev *);
int input_register_device(struct input_dev *);
void input_unregister_device(struct input_dev *);
int sysfs_create_group(struct kobject *, const struct attribute_group *);
void sysfs_remove_group(struct kobject *, const struct attribute_group *);
int request_threaded_irq(int, void *, irqreturn_t (*)(int, void *), int, const char *, void *);
void free_irq(int, void *);
int i2c_add_driver(struct i2c_driver *);
void i2c_del_driver(struct i2c_driver *);
#endif
