# Breaking Change: GATEWAY_SIGNING_KEY Minimum Length

## What changed
The minimum allowed length for `GATEWAY_SIGNING_KEY` is now 32 bytes. This aligns with RFC 7518 Section 3.2 for HS256 JWT signatures and is enforced across both session encryption and JWT signing. The gateway will now fail fast at startup if a key shorter than 32 bytes is provided.

## Migration Steps
If your deployment previously used a `GATEWAY_SIGNING_KEY` shorter than 32 bytes, you must generate and provide a new key.

1. Generate a new secure 32-byte key:
   ```bash
   openssl rand -base64 32
   ```
2. Update your gateway configuration or deployment to use the newly generated key for `GATEWAY_SIGNING_KEY`.
3. Restart the gateway. Note that because this is a breaking change to the signing key, existing sessions and backend initialization tokens will become invalid, and users will need to re-authenticate.
