# Apple Music Developer Token Design

## Goal

Allow the backend to sign its own Apple Music developer token when a Media Services private key is configured. A complete signing configuration must bypass the existing `music.apple.com` JavaScript token discovery path.

## Configuration

Add these optional fields under `catalog`:

```yaml
apple_music_private_key_path: "keys/AuthKey_88KBJL3CKU.p8"
apple_music_key_id: "88KBJL3CKU"
apple_music_team_id: "2VTXNMR2GL"
```

The three fields form one configuration unit:

- If all three are empty, developer-token signing is disabled and the legacy token paths remain available.
- If all three are present, developer-token signing is enabled.
- If only some are present, configuration loading fails with a clear error.

The key path, key ID, and team ID may be stored in `config.yaml`. The `.p8` contents remain in a separate ignored or externally mounted file.

## Token Selection

`CatalogClient.Token` uses this precedence:

1. With complete developer-token configuration, sign and cache a developer token locally. Signing or key-loading errors are returned directly. The client must not fetch Apple website HTML or JavaScript and must not fall back to another token source.
2. Without developer-token configuration, use a configured `authorization_token` directly.
3. If neither source is configured, retain the legacy `music.apple.com` JavaScript discovery behavior.

This also changes the existing `authorization_token` behavior from fallback-after-scrape to an explicit override that avoids the website request.

## Signing and Caching

- Parse the `.p8` file as a PKCS#8 ECDSA private key.
- Sign a JWT with ES256.
- Header claims: `alg: ES256`, configured `kid`.
- Payload claims: configured `iss`, current Unix `iat`, and `exp` 24 hours after issuance.
- Do not add an `origin` claim because this token is used by the backend.
- Cache the token in memory and renew it five minutes before expiration.
- A backend restart creates a new token on the first request.
- Never log the private key or complete token.

## Error Handling

Configuration validation rejects partial signing configuration. With complete configuration, missing files, unreadable files, invalid PEM/PKCS#8 data, non-ECDSA keys, and signing failures return contextual errors. None of these errors trigger website-token discovery.

## Testing

Tests will cover:

- loading complete, absent, and partial signing configuration;
- ES256 JWT header, claims, signature, and 24-hour lifetime;
- cached token reuse and renewal behavior;
- configured signing bypassing all HTTP token-discovery requests;
- signing failure returning an error without fallback;
- legacy explicit-token and website-discovery behavior when signing is disabled.

