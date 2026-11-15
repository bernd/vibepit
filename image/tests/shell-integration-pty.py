"""Exercise prompt hooks through interactive Bash's actual PS0/PS1 expansion."""
import os
import pty
import select
import shlex
import subprocess
import tempfile
import time
from pathlib import Path
from urllib.parse import quote

integration = Path(__file__).resolve().parent.parent / "config/shell-integration.bash"
with tempfile.TemporaryDirectory() as temporary:
    directory = Path(temporary) / "space 世界"
    directory.mkdir()
    rc = Path(temporary) / "bashrc"
    rc.write_text('PS1="test> "\nPROMPT_COMMAND=\'printf "hook-status=%s\\n" "$?"\'\n'
                  f"source {shlex.quote(str(integration))}\n")
    master, slave = pty.openpty()
    process = subprocess.Popen(
        ["bash", "--noprofile", "--rcfile", str(rc), "-i"],
        stdin=slave, stdout=slave, stderr=slave, start_new_session=True,
        env={**os.environ, "VIBEPIT_SHELL_INTEGRATION": "1", "TERM": "xterm-256color"},
    )
    os.close(slave)

    def prompt():
        output = b""
        deadline = time.monotonic() + 5
        while b"\x1b]133;B\x07" not in output:
            if time.monotonic() > deadline:
                raise AssertionError(f"prompt timeout: {output!r}")
            if select.select([master], [], [], 0.1)[0]:
                output += os.read(master, 65536)
        return output

    try:
        initial = prompt()
        assert b"\x1b]133;A\x07" in initial, initial
        os.write(master, b"false\n")
        failed = prompt()
        assert failed.count(b"\x1b]133;C\x07") == 1, failed
        assert b"\x1b]133;D;1\x07" in failed, failed
        assert b"hook-status=1" in failed, failed
        assert b"\x01" not in failed and b"\x02" not in failed, failed
        os.write(master, b"printf 'previous=%s\\n' \"$?\"\n")
        assert b"previous=1" in prompt()
        os.write(master, f"cd {shlex.quote(str(directory))}\n".encode())
        changed = prompt()
        assert quote(str(directory)).encode() in changed, changed
        assert b"\x1b]133;D;0\x07" in changed, changed
        os.write(master, b"exit\n")
        process.wait(timeout=5)
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()
        os.close(master)
