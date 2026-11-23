from pathlib import Path
import hashlib

root = Path(__file__).resolve().parent.parent
expected, relative = (root / 'CHECKSUMS.sha256').read_text().strip().split(None, 1)
actual = hashlib.sha256((root / relative).read_bytes()).hexdigest()
if actual != expected:
    raise SystemExit('Controller checksum mismatch')
print('Controller SHA-256 verified')
