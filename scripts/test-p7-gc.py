#!/usr/bin/env python3
"""Reproduce the macOS synthetic GC SIGKILL drill; no HealthKit/network access."""
import pathlib
import signal
import subprocess
import tempfile

root = pathlib.Path(__file__).resolve().parents[1]
package = root / "ios/RAI"
subprocess.run(["swift", "build", "--product", "GCProbe"], cwd=package, check=True)
bin_dir = subprocess.check_output(["swift", "build", "--show-bin-path"], cwd=package, text=True).strip()
probe = str(pathlib.Path(bin_dir) / "GCProbe")
with tempfile.TemporaryDirectory(prefix="rai-p7-gc-") as directory:
    subprocess.run([probe, "seed", directory], check=True)
    sweep = subprocess.Popen([probe, "sweep", directory], stdout=subprocess.PIPE, text=True)
    try:
        assert sweep.stdout.readline().strip() == "GC_BATCH_COMPLETE"
        sweep.kill()
        assert sweep.wait(timeout=10) == -signal.SIGKILL
    finally:
        if sweep.poll() is None:
            sweep.kill()
            sweep.wait(timeout=10)
        sweep.stdout.close()
    subprocess.run([probe, "verify", directory], check=True)
    print("SIGKILL_EXIT=-9; anchor preserved; marked payloads reclaimed after reopen")
