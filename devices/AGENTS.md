# Device package boundaries

- Assemble each device package explicitly from the shared engine, backend adapters, the appropriate platform host and its own device integration. Connect device behavior to the engine through shared interfaces; keep the core free of device-model branches and imports of concrete device modules.
- Reuse existing interfaces when adding packaging or device integration. If a required behavior has no suitable contract, work with the shared contract's owner to define the smallest interface needed by that implementation. Define interfaces as real needs arise, not a universal device framework in advance.
- Keep hardware wiring, calibration, measured tuning and packaging inputs in the device module. Keep reusable OS integration in `platforms/` and shared engine behavior in `core/`. A package must not pull in another device's integration or settings.
- Add profile fields only when an implemented consumer needs them and their values are established. Record unresolved requirements in documentation instead of adding placeholder settings or capability claims.
- Follow [the package contract](../docs/device-packages/README.md) for the planning-profile format and validation commands. Metadata checks do not establish hardware compatibility or installability.
