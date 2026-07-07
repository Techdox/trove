# Dashboard authentication

Trove is still read-only, but the dashboard and read APIs can be protected with native OpenID Connect (OIDC) authentication.

When OIDC is not configured, Trove keeps the original behaviour: the dashboard and read APIs are open. In that mode, keep the server on a trusted network, VPN, tailnet, or behind an authenticating reverse proxy.

## What OIDC protects

When `TROVE_OIDC_ISSUER` and the other required OIDC settings are present, Trove protects:

| Method | Path | Auth behaviour |
| --- | --- | --- |
| `GET` | `/` | Browser users are redirected to the identity provider. |
| `GET` | `/api/v1/services` | Requires OIDC session or `TROVE_API_TOKEN`. |
| `GET` | `/api/v1/agents` | Requires OIDC session or `TROVE_API_TOKEN`. |
| `GET` | `/api/v1/events` | Requires OIDC session or `TROVE_API_TOKEN`. |
| `GET` | `/api/v1/me` | Requires OIDC session or `TROVE_API_TOKEN`; returns current auth state. |

OIDC does **not** protect these routes:

| Method | Path | Why |
| --- | --- | --- |
| `POST` | `/api/v1/report` | Agent ingest keeps using per-agent bearer tokens. |
| `GET` | `/healthz` | Container and reverse proxy health checks need unauthenticated access. |
| `GET` | `/oauth2/login` | Starts the login flow. |
| `GET` | `/oauth2/callback` | Receives the provider callback. |
| `POST` | `/oauth2/logout` | Clears the local Trove session and redirects to provider logout when available. |

## Required environment variables

Set these on the `trove-server` process:

| Variable | Purpose |
| --- | --- |
| `TROVE_OIDC_ISSUER` | OIDC issuer/discovery URL. Example: `https://auth.example.com/application/o/trove/` |
| `TROVE_OIDC_CLIENT_ID` | OAuth2/OIDC client ID from the provider. |
| `TROVE_OIDC_CLIENT_SECRET` | OAuth2/OIDC client secret from the provider. |
| `TROVE_OIDC_REDIRECT_URL` | Trove callback URL. Example: `https://trove.example.com/oauth2/callback` |

Optional:

| Variable | Default | Purpose |
| --- | --- | --- |
| `TROVE_API_TOKEN` | unset | Static bearer token for scripts/API clients that cannot use browser OIDC. |
| `TROVE_OIDC_SESSION_MAX_AGE` | `8h` | Signed dashboard session lifetime. Uses Go duration syntax such as `4h`, `12h`, or `30m`. |

Example:

```env
TROVE_OIDC_ISSUER=https://auth.example.com/application/o/trove/
TROVE_OIDC_CLIENT_ID=trove
TROVE_OIDC_CLIENT_SECRET=change-me
TROVE_OIDC_REDIRECT_URL=https://trove.example.com/oauth2/callback
TROVE_API_TOKEN=<your-api-token>
TROVE_OIDC_SESSION_MAX_AGE=8h
```

The session cookie is signed using the OIDC client secret. It is `HttpOnly`, `SameSite=Lax`, and marked `Secure` when the configured redirect URL is HTTPS.

## Authentik setup

In Authentik, create an OAuth2/OpenID provider for Trove.

Recommended provider settings:

| Setting | Value |
| --- | --- |
| Provider type | OAuth2/OpenID Provider |
| Client type | Confidential |
| Redirect URI | `https://trove.example.com/oauth2/callback` |
| Scopes | `openid`, `profile`, `email` |
| Signing key | Use your normal Authentik signing key |

Create an Authentik application that uses that provider, then expose it through your normal Authentik outpost/application flow.

For logout to return cleanly after terminating the upstream SSO session, make sure Authentik allows the dashboard root as a post-logout return URI as well as the callback URL:

```text
https://trove.example.com/oauth2/callback
https://trove.example.com/
```

Trove discovers Authentik's `end_session_endpoint` from OIDC metadata. On logout, Trove clears its local `trove_session` cookie and redirects the browser to that endpoint with `client_id` and `post_logout_redirect_uri` set.

## Login flow

1. A browser requests `/` or one of the read APIs.
2. If there is no valid `trove_session` cookie, Trove redirects to `/oauth2/login`.
3. Trove sends the browser to the provider authorization endpoint.
4. The provider redirects back to `/oauth2/callback`.
5. Trove validates the state value, exchanges the code for tokens, verifies the ID token, and sets a signed session cookie.
6. The browser returns to the original path or `/`.

API clients get a JSON `401` instead of a browser redirect when they send an `Authorization` header or do not ask for `text/html`.

## Logout flow

The dashboard logout button submits a real `POST` form to `/oauth2/logout`. This matters: a `fetch()` logout would follow redirects in the background and leave the browser on the dashboard.

On `POST /oauth2/logout`, Trove:

1. clears the local `trove_session` cookie;
2. redirects to the provider's discovered `end_session_endpoint` when it exists;
3. includes `client_id` and `post_logout_redirect_uri`;
4. falls back to the dashboard root if the provider does not publish a valid logout endpoint.

With Authentik, the redirect target usually looks like:

```text
https://auth.example.com/application/o/trove/end-session/?client_id=trove&post_logout_redirect_uri=https%3A%2F%2Ftrove.example.com%2F
```

After the provider logout finishes, the user lands back at the dashboard root and will need to sign in again.

## Programmatic API access

If OIDC is enabled and you want scripts to query read APIs, set `TROVE_API_TOKEN` and send it as a bearer token:

```sh
curl -H "Authorization: Bearer <your-api-token>" \
  https://trove.example.com/api/v1/services
```

This token bypasses OIDC only for the dashboard/read API group. It does not replace per-agent tokens for `POST /api/v1/report`.

## Verify

Check OIDC discovery and server startup first:

```sh
docker compose logs server | grep -i oidc
```

Unauthenticated browser requests should redirect to login:

```sh
curl -I -H 'Accept: text/html' https://trove.example.com/
```

API requests without a token should return `401`:

```sh
curl -i https://trove.example.com/api/v1/services
```

API requests with `TROVE_API_TOKEN` should return JSON:

```sh
curl -H "Authorization: Bearer <your-api-token>" \
  https://trove.example.com/api/v1/services
```

Logout should redirect through the provider logout endpoint:

```sh
curl -i -X POST https://trove.example.com/oauth2/logout
```

Look for a `303 See Other` and a `Location` header pointing at the provider's end-session endpoint.

## Troubleshooting

### Login redirects fail

Check that `TROVE_OIDC_REDIRECT_URL` exactly matches the callback URL registered with the provider, including scheme, hostname, path, and trailing slash behaviour.

### Logout signs straight back in

That usually means the provider SSO session was not terminated. Check that the provider publishes `end_session_endpoint` in its discovery metadata:

```sh
curl https://auth.example.com/application/o/trove/.well-known/openid-configuration
```

Also check that the dashboard root URL is allowed as a post-logout redirect/return URI in the provider.

### API scripts get redirected instead of JSON

Send either an `Authorization` header or an `Accept: application/json` header. Browser-style requests with `Accept: text/html` are redirected into the login flow.

### Health checks fail after enabling OIDC

Use `/healthz`. It remains unauthenticated by design.
