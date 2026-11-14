# SNMP bridge

## Summary

The following list gives the main facts of this document.

- The bridge opens one WebSocket to the hub for each Target. All Targets use one permanent universal token.
- The hub names a Target by its fingerprint, which comes from the Target `id`. A changed `id` gives a new system, and the old system shows as down.
- A changed `TOKEN` gives a duplicate system for each Target.
- A system that you delete in the hub comes back while its Target is in the config.
- `SNMPBRIDGE_CONFIG` names the config file, in the YAML data format, with the Profiles and the Targets.

## Scope

This document explains how the Simple Network Management Protocol (SNMP) bridge connects to a Beszel hub. It is an explanation for the person who runs the bridge.

This version covers the token, the identity of a Target and the config file. It does not cover the table reads or the read timing. It does not cover how the bridge sets a Target down.

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

## Config

The bridge reads its Targets and Profiles from one config file in YAML format. It loads the file before it opens a WebSocket. When the file has an error, the bridge writes the error to stderr and exits with code 1.

### Variables

The following table shows the names that the config uses.

| Name | Where | Holds |
|---|---|---|
| `SNMPBRIDGE_CONFIG` | Environment | The path of the config file. It has no default. |
| `community_env` | Target | The name of a variable that holds the SNMP community. |
| `community_file` | Target | The path of a file that holds the SNMP community. |

A Target has exactly one of `community_env` and `community_file`. An empty or missing variable or file stops the start.

The community never goes to a log or an error text.

### Example

The following file has one Profile and one Target. The host is an example value.

```yaml
profiles:
  switch:
    cpu: ucd
    memory: ucd
    tables:
      - oid: .1.3.6.1.4.1.99.1.1
        name_column: 2
        value_column: 5
        kind: fan
        scale: 10
        match: '^Fan'
        exclude: 'spare$'
targets:
  - id: switch-1
    name: Switch 1
    host: 192.0.2.10
    port: 161
    version: 2c
    profile: switch
    community_file: /run/secrets/switch-1-community
```

### Rules of the file

The top level has only `profiles` and `targets`. An unknown key at any level stops the start, and the error names the key.

The following table lists the fields of a Profile and of a table rule.

| Field | Where | Rule |
|---|---|---|
| `cpu`, `memory` | Profile | Optional. The only valid value is `ucd`. |
| `tables` | Profile | A list of table rules. A Profile needs a table rule, `cpu` or `memory`. |
| `oid` | Table rule | Required. The table entry object identifier (OID), dotted. |
| `name_column`, `value_column` | Table rule | Required. Column numbers below the entry OID. |
| `kind` | Table rule | Required. `temperature` or `fan`. |
| `scale` | Table rule | A number more than 0 that divides the value. Default 1. |
| `match`, `exclude` | Table rule | One Go regular expression each. Optional. |

The following table lists the fields of a Target.

| Field | Rule |
|---|---|
| `id` | Required and unique. Only `a-z`, `0-9` and `-`. |
| `name` | Required. |
| `host` | Required. |
| `port` | Default 161. |
| `version` | Default `2c`. The only valid value is `2c`. |
| `profile` | Required. It must name a Profile. |

### Built-in sources

The `ucd` source reads the objects of the University of California, Davis (UCD) SNMP Management Information Base (MIB). The following list gives the formulas.

- `cpu: ucd` gives 100 minus `ssCpuIdle`, limited to 0 to 100.
- `memory: ucd` gives (`memTotalReal` minus `memAvailReal` minus `memBuffer` minus `memCached`) divided by `memTotalReal`.

The memory source gives no data in these cases:

- `memTotalReal` is 0 or missing.
- `memAvailReal` is missing.
- The used memory is below 0.

A missing `memBuffer` or `memCached` counts as 0.

### Changed id of a Target

The Target `id` sets the fingerprint, so a changed `id` of a Target gives a new system. The old system shows as down. Delete the old system in the hub.

### Status of this version

This version loads and checks the config and has the SNMP reader. It does not read the tables. Each Target reports the fixed data of a stub.
