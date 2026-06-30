# Swagger API Docs Implementation Plan

**Goal:** Add interactive Swagger UI and a complete OpenAPI contract without new runtime dependencies.

1. Add failing tests for `/docs` and `/api/openapi.yaml`, including required paths and schema parsing.
2. Embed the OpenAPI YAML and serve a pinned Swagger UI HTML page.
3. Document the UI URL, run formatting, vet, race tests, and verify the live page after restart.
