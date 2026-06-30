# Project Rules

- Whenever you finish editing backend code, always restart the backend API server.
  - Terminate any running backend process on port `18080` (e.g. by identifying it with `lsof -i :18080`).
  - Start the backend via `go run ./cmd/amdl-api` in the background from the `backend` directory.
