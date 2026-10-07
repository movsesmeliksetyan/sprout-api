# Auth0 setup

How to configure an Auth0 tenant for Sprout. Do this once per environment (dev, staging, prod), each with its own tenant. Dashboard labels move around; the names below are the ones to look for.

The API itself needs two values from this setup:

| Key | Value |
|---|---|
| `AUTH0_DOMAIN` | The tenant hostname without scheme, e.g. `sprout-dev.eu.auth0.com`. If the tenant uses a custom domain, use that instead: it is what appears in the token's `iss`. |
| `AUTH0_AUDIENCE` | The API identifier from step 1, e.g. `https://api.sprout.app`. |

## 1. API

*Applications → APIs → Create API.*

- **Name:** Sprout API.
- **Identifier:** a URL-shaped string such as `https://api.sprout.app`. It is never fetched, cannot be changed later, and becomes `AUTH0_AUDIENCE`.
- **Signing algorithm:** RS256. The API rejects everything else.
- In the API's settings, turn on **Allow Offline Access** so the iOS app can get refresh tokens.

## 2. Native application (iOS)

*Applications → Applications → Create Application → Native.*

- **Allowed Callback URLs** and **Allowed Logout URLs:** the two URLs Auth0.swift uses, with the app's bundle identifier:
  `https://<AUTH0_DOMAIN>/ios/<bundle id>/callback` and `<bundle id>://<AUTH0_DOMAIN>/ios/<bundle id>/callback`.
- **Refresh Token Rotation:** on.
- *Advanced Settings → Grant Types:* Authorization Code and Refresh Token, plus Passwordless OTP if step 4 uses passwordless.
- *Advanced Settings → Device Settings → iOS:* the Apple Team ID and the app's bundle identifier. Native Sign in with Apple needs both.

The iOS app must request the API's audience when it logs in. Without it Auth0 issues an opaque token that this API cannot validate.

## 3. Sign in with Apple

*Authentication → Social → Create Connection → Apple.*

Fill in the Apple Team ID, the Services ID as client ID, and the Key ID and private key of a Sign in with Apple key from the Apple Developer account. Request the `name` and `email` scopes, then enable the connection for the native application.

Apple shares the user's name only on the very first login and may share a private relay address as the email.

## 4. Email login

Either of these; which one is still open (PRD open question 2). The API does not care.

- **Passwordless (preferred):** *Authentication → Passwordless → Email*, one-time code, enabled for the native application.
- **Database:** *Authentication → Database → Create DB Connection*, enabled for the native application.

## 5. Action: email and name in the access token

Access tokens carry only `sub` by default. The API creates a user on their first request and reads their email and name from the token, so an Action has to put them there. Auth0 drops custom claims that are not namespaced; the namespace below must match `ClaimsNamespace` in `internal/auth/claims.go`.

*Actions → Library → Create Action → Build from scratch*, trigger **Login / Post Login**:

```js
exports.onExecutePostLogin = async (event, api) => {
  const namespace = 'https://sprout.app/';
  if (event.user.email) {
    api.accessToken.setCustomClaim(`${namespace}email`, event.user.email);
  }
  if (event.user.name) {
    api.accessToken.setCustomClaim(`${namespace}name`, event.user.name);
  }
};
```

Deploy it, then add it to the flow under *Actions → Triggers → post-login*. An Action that is deployed but not in the flow does nothing.

Both claims are optional to the API: a token without them is accepted and the user is created without an email or name.

## 6. Machine-to-machine application (account deletion)

*Applications → Applications → Create Application → Machine to Machine*, authorised for the **Auth0 Management API** with the single permission `delete:users`.

The worker uses it to delete the Auth0 identity when a user deletes their account (BE-60, which also adds the configuration keys for its client id and secret). It must not be authorised for the Sprout API.

## What the API checks

Every `/v1` request must carry `Authorization: Bearer <access token>`. The token is accepted only if all of these hold:

| Check | Expected |
|---|---|
| Algorithm | RS256 |
| Signature | A key in the tenant's JWKS (`https://<AUTH0_DOMAIN>/.well-known/jwks.json`) |
| `iss` | `https://<AUTH0_DOMAIN>/`, trailing slash included |
| `aud` | Contains `AUTH0_AUDIENCE` |
| `exp`, `nbf`, `iat` | Valid now, with 30 s of clock tolerance |
| `sub` | Present, and not a machine token (`…@clients`) |

Anything else gets `401 unauthenticated`. If the tenant's keys cannot be fetched the answer is `503 unavailable`, so that an Auth0 outage does not sign users out.

The keys are cached for 15 minutes (or for as long as the JWKS response's `Cache-Control` says). Auth0 publishes the next signing key before it starts using it, so *Settings → Signing Keys → Rotate* needs no restart. After **revoking** a key, tokens signed with it can still be accepted until the cache refreshes.

## Checking the setup

Log in through the iOS app (or any client of the native application that requests the audience) and call the API with the access token:

```
curl -i -H "Authorization: Bearer $TOKEN" http://localhost:8080/v1/me
```

`401` means the token was rejected; run the API with `LOG_LEVEL=debug` and look for the `token rejected` line, whose `reason` names the failed check. The token on the API's *Test* tab in the dashboard is a machine token and is rejected by design.
