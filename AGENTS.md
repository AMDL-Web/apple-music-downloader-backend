# AGENTS.md

# Project Overview

This repository is the **primary backend implementation** of an Apple Music download and wrapper-manager system.

It is actively evolving and serves as the **single source of truth for production behavior**.

---

# 1. Primary System (Source of Truth)

## backend/

All implementation work MUST be done in this directory unless explicitly stated otherwise.

---

# 2. Reference Systems (Read-Only, Design Guidance Only)

These repositories are NOT to be modified. They are used for architectural and behavioral reference only.

---

## Reference A — explore/amdl-myversion

Python + Go hybrid system:

- Python orchestration layer
- Go downloader module
- Web UI frontend
- Multi-user architecture

---

## Reference B — explore/apple-music-downloader-main

Original Go CLI implementation (Sorrow / zhaarey)

---

## Reference C — explore/AppleMusicDecrypt-2

Python-based system (WorldObservationLog)

---

# 3. Development Rules

- This repository is in active development.
- Destructive schema and data changes are allowed when necessary for iteration and refactoring.
- Prefer correctness and architecture improvement over backward compatibility.
- When making breaking changes, ensure all affected modules are updated consistently.
- If database schema changes are required, explain migration impact before applying.

---

# 4. Implementation Constraints

- backend/ is the only writable production codebase
- Reference repositories are read-only
- Do not copy code blindly; always adapt to backend/ architecture
- Keep changes minimal and consistent with existing patterns
- Avoid unnecessary refactoring unless explicitly requested

---

# End of AGENTS.md