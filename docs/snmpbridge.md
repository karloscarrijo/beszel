# SNMP bridge

## Summary

The following list gives the main facts of this document.

- The bridge opens one WebSocket to the hub for each Target.
- All Targets use one permanent universal token.
- The hub names a Target by its fingerprint, which comes from the Target `id`.
- A changed `TOKEN` gives a duplicate system for each Target.
- A system that you delete in the hub comes back while its Target is in the config.

## Scope

This document explains how the Simple Network Management Protocol (SNMP) bridge connects to a Beszel hub. It is an explanation for the person who runs the bridge.

This version covers the token and the identity of a Target. It does not cover the config file, the Profiles, the SNMP reads or the down status of a Target.

## Token and identity

The bridge reads these variables, with the same rules as the stock agent:

- `HUB_URL`
- `TOKEN` or `TOKEN_FILE`
- `KEY` or `KEY_FILE`

The bridge stops at the start with an error that names the variable when the token or the key is missing.

The following list shows what the hub stores for each Target.

- **Fingerprint:** the first 24 bytes of the Secure Hash Algorithm 256-bit (SHA-256) sum of the Target `id`, as hex.
- **System name:** the Target `name`.
- **Token:** the hub stores the value of `TOKEN` in a fingerprint record.

At the next connect, the hub finds the fingerprint record of the Target and uses the same system. So a restart of the bridge makes no duplicate system.

### Permanent universal token

Do these steps to set the token:

1. Turn on the permanent universal token in the hub settings, in "Tokens and fingerprints".
2. Copy its value to `TOKEN`.

If you turn the token off, a known Target keeps its system and connects as before. A new Target cannot connect. The bridge then writes a log line with the Target and the text "turn on the permanent universal token".

Do not turn the permanent universal token off and on. The hub then makes a new token value.

### Changed token value

A changed `TOKEN` gives a duplicate system for each Target. The hub finds the fingerprint records by their token. The records hold the old value, so the hub treats each Target as new.

The hub settings page can only make a random token for a record ("Rotate token"). It has no field for a chosen value. So the repair uses the PocketBase page of the hub.

Stop the bridge first. A change of a fingerprint record sets a connected system down. Then do these steps:

1. Stop the bridge.
2. Open the PocketBase page of the hub at `/_/`, and sign in as a superuser.
3. Open the collection `fingerprints`.
4. Set the field `token` of each fingerprint record of the bridge to the new value.
5. Set the new value in `TOKEN`.
6. Start the bridge.
7. Check that each Target shows in its old system and that no duplicate system exists.

If you already have duplicate systems, delete them in the hub after the repair.

A test saves the new value in the fingerprint records in code, and shows that each Target then uses its old system. No test used the PocketBase page itself.

### System deletion

A system that you delete in the hub comes back at the next connect while its Target is in the config. Do these steps to remove a Target:

1. Remove the Target from the config.
2. Delete the system in the hub.
