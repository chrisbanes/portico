#!/usr/bin/python3
"""The VM proof's manual-recovery steps, kept apart so they run against real
Go output without a VM (TestRecoveryScriptAgainstAGuardedCommit).

  recovery.py verify ROOT   no post-cutover mutation, identities unchanged
  recovery.py restore ROOT  restore each guarded record from the checkpoint
"""
import hashlib
import json
import os
import sys


def sha(path):
    with open(path, "rb") as f:
        return hashlib.sha256(f.read()).hexdigest()


def load(root, rel):
    with open(os.path.join(root, rel)) as f:
        return json.load(f)


def verify(root):
    m = load(root, "service/manifest.json")
    gen = os.path.join(root, "service/generations", m["generation"])
    state = load(gen, "state.json")
    assert not state["migration"].get("firstMutationRequestID"), "a post-cutover mutation was accepted"
    # The generation's identity tree must be exactly what was checkpointed:
    # an entry the runtime added after cutover, such as a new node's state,
    # would be lost by restoring, so it stops recovery as a change would.
    expected = {rel: want for rel, want in m["sources"].items() if rel.startswith("tsnet/")}
    actual = {}
    def unreadable(err):
        raise err  # an unreadable entry must stop recovery, not pass it

    for dirpath, dirnames, filenames in os.walk(os.path.join(gen, "tsnet"), onerror=unreadable):
        for name in dirnames + filenames:
            full = os.path.join(dirpath, name)
            rel = os.path.relpath(full, gen)
            if os.path.islink(full):
                actual[rel] = "link"
            elif os.path.isdir(full):
                actual[rel] = "dir"
            else:
                actual[rel] = sha(full)
    added = sorted(set(actual) - set(expected))
    removed = sorted(set(expected) - set(actual))
    changed = sorted(r for r in set(actual) & set(expected) if actual[r] != expected[r])
    assert not (added or removed or changed), (
        "the identities changed since the checkpoint: added %s, removed %s, changed %s" % (added, removed, changed))
    print("PASS recovery: no post-cutover mutation, identities unchanged since the checkpoint")


def restore(root):
    cp = load(root, "service/guard/checkpoint.json")
    for name in ("installation-v5.json", "installation-v4.json"):
        target = os.path.join(root, name)
        os.chflags(target, 0)
        original = cp["files"][name]
        if not original["present"]:
            os.remove(target)
            continue
        with open(os.path.join(root, "service/guard/originals", name), "rb") as f:
            data = f.read()
        assert hashlib.sha256(data).hexdigest() == original["sha256"], name
        with open(target + ".restore", "wb") as f:
            f.write(data)
            os.fchmod(f.fileno(), original["mode"])
            os.fsync(f.fileno())
        os.rename(target + ".restore", target)


if __name__ == "__main__":
    if len(sys.argv) != 3 or sys.argv[1] not in ("verify", "restore"):
        sys.exit("usage: recovery.py verify|restore ROOT")
    {"verify": verify, "restore": restore}[sys.argv[1]](sys.argv[2])
