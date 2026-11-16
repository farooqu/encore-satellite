"""Check module-local dependency direction, including tests, without third-party tools."""

import json
import subprocess

MODULE = "github.com/farooqu/encore-satellite/"
packages = subprocess.check_output(["go", "list", "-json", "./..."], text=True)
decoder = json.JSONDecoder()
errors = []
while packages.strip():
    package, end = decoder.raw_decode(packages.lstrip())
    packages = packages.lstrip()[end:]
    owner = package["ImportPath"].removeprefix(MODULE)
    imports = package.get("Imports", []) + package.get("TestImports", []) + package.get("XTestImports", [])
    for dependency in imports:
        if not dependency.startswith(MODULE):
            continue
        target = dependency.removeprefix(MODULE)
        allowed = True
        if owner.startswith("core/"):
            allowed = target.startswith("core/")
        elif owner.startswith("backends/"):
            backend = owner.split("/")[1]
            allowed = target == f"backends/{backend}" or target.startswith(("core/", f"backends/{backend}/", "backends/internal/"))
        elif owner.startswith("platforms/"):
            platform = owner.split("/")[1]
            allowed = target == f"platforms/{platform}" or target.startswith(("core/", f"platforms/{platform}/"))
        elif owner.startswith("devices/"):
            device = owner.split("/")[1]
            allowed = not target.startswith("devices/") or target == f"devices/{device}" or target.startswith(f"devices/{device}/")
        if not allowed:
            errors.append(f"{owner} must not import {target}")
if errors:
    raise SystemExit("\n".join(errors))
print("Dependency boundaries passed (including test imports)")
