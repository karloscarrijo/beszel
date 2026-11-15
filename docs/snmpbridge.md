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

This version covers the token, the identity of a Target, the config file and the table rules of a Profile. It also covers the read timing, the down status and the image.

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
4. Set the field `token` of each old fingerprint record of the bridge to the new value. These records hold the old token. In the edit panel, change the field, click "Unlock to save", and then click "Save changes".
5. Set the new value in `TOKEN`.
6. Start the bridge.
7. Check that each Target shows in its old system and that no duplicate system exists.

If you already have duplicate systems, delete them in the hub after the repair.

A test saves the new value in the fingerprint records in code, and shows that each Target then uses its old system.

A browser test also did these steps on the PocketBase page of the stock hub 0.21.2, with PocketBase 0.42.2. It started with 2 duplicate systems.

After the repair, each Target used its old system, and the 2 duplicate systems showed as down. The delete of the duplicate systems did not change the old systems.

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

### Example for the 2 Targets

The file `internal/snmpbridge/testdata/bridge.yaml` is one complete config for the 2 Targets of the local stack: `nas-bmc` and `cgw-fiber`. It reads the communities from variables.

The file `internal/snmpbridge/testdata/compose.yaml` runs it with snmpsim and the stock hub.

```yaml
profiles:
  nas-bmc:
    tables:
      - oid: .1.3.6.1.4.1.21317.1.3.1
        name_column: 13
        value_column: 2
        kind: temperature
        match: 'Temp$'
        exclude: '^(CPU2|Vcpu2VRM|VmemEFVRM|VmemGHVRM|P[12]-DIMM[C-H])'
      - oid: .1.3.6.1.4.1.21317.1.3.1
        name_column: 13
        value_column: 2
        kind: fan
        match: '^FAN'
        exclude: '^FAN(1|3|4|A|B)$'
  cgw-fiber:
    cpu: ucd
    memory: ucd
    tables:
      - oid: .1.3.6.1.4.1.2021.13.16.2.1
        name_column: 2
        value_column: 3
        kind: temperature
        scale: 1000
        exclude: '^temp3$'
      - oid: .1.3.6.1.4.1.2021.13.16.3.1
        name_column: 2
        value_column: 3
        kind: fan
        exclude: '^fan2$'
targets:
  - id: nas-bmc
    name: NAS BMC
    host: snmpsim
    profile: nas-bmc
    community_env: NAS_BMC_COMMUNITY
  - id: cgw-fiber
    name: CGW-Fiber
    host: snmpsim
    profile: cgw-fiber
    community_env: CGW_FIBER_COMMUNITY
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

### Hub fields of a Target

The following list gives the hub fields that a Target fills. The agent of Beszel fills these fields in the same way. A Target fills no other field, such as disk or network.

- CPU percent, with 2 decimals, from `cpu`.
- Memory percent, with 2 decimals, from `memory`.
- Memory in bytes (total, used, and buffer plus cached), from `memory`. The `memory` source gives the UCD values in kB, and the bridge multiplies them by 1024. The Memory chart of the hub reads these bytes.
- Uptime in seconds, from `sysUpTime`.
- Fans and temperatures, from the table rules.
- Dashboard temperature, from the table rules. It is the highest temperature of the read, after `match`, `exclude` and `scale`. The "Temp" column of the system list shows it. The Temperature alert of the hub uses the highest sensor of the Target. A read with no temperature above 0 gives 0, as the agent of Beszel does. The hub then shows no value and sends no alert.

The uptime is the uptime of the SNMP service of the Target, not of the device. It has no config key.

A missing `sysUpTime` or a failed read of it gives 0, and the Target stays up. The hub hides an uptime of 0.

A Target with no `cpu` or no `memory` source shows 0 % for that value. This is not an error.

### Changed id of a Target

The Target `id` sets the fingerprint, so a changed `id` of a Target gives a new system. The old system shows as down. Delete the old system in the hub.

## Profiles

A Profile names the tables that the bridge reads from a Target. The bridge puts the fans in `Stats.Fans` and the temperatures in `Stats.Temperatures`. The hub then draws the charts with no UI change.

A sensor with no data point has no key in these maps.

### Steps of a table rule

The bridge does these steps for each table rule, in this order:

1. Walk the table below `oid`.
2. Join the name column and the value column by the row index. The row index is the part of the OID after the column number.
3. Keep a row when its name matches `match` and does not match `exclude`. Both are searches in the name, not matches of the full name. The bridge drops a row with an empty name.
4. Add a suffix to a repeated name. The second row of the name `FAN1` becomes `FAN1 #2`, and the bridge logs one warning for the name.
5. Read the value. It is an integer, or a string with a decimal number such as `"700.000"`. The decimal point is a dot.
6. Divide the value by `scale`.
7. Check the range. A fan value must be an integer from 0 to 65535. A temperature must be a finite number.

A value that fails a step gives no data point. It never wraps to a different number. It does not stop the bridge.

The bridge keeps the name with its row. So a sensor keeps its name and value when the device gives it a new index.

### Logs and failed reads

The following list gives the log lines of the table rules. The bridge writes each line for a change, not for each read.

- **Sensor gone:** a name that gave data since the start and is not in the data now gives one warning. When the name comes back, the bridge logs one line.
- **Rule with no data:** a rule with no data point gives one warning that names the Target and the rule. The rule number is its position in the Profile, from 1. The bridge logs again only when the state changes.
- **Repeated name:** one warning for each repeated name. The suffix applies over all rules of one kind (fan or temperature) of the Target.
- **Failed table read:** a rule whose walk fails gives one warning for each run of failures. The other rules still give data.

A rule with no data does not stop the other rules. When no rule of the Profile gives data (the table rules, `cpu` and `memory` together), the read fails. The bridge then replies to the hub with an error, and the hub shows the system as down.

The known limit of the suffix: after a firmware update, `FAN1` and `FAN1 #2` can change places. Their chart history then mixes.

### Example for the NAS BMC

The baseboard management controller (BMC) of the network-attached storage (NAS) has one vendor table. Column 13 holds the name. Column 2 holds the value as a string.

Some rows are not real sensors. `FAN1` always reads 0, and some temperature rows read 0 for a part that is not there. The `exclude` expressions remove them.

The community and the host are example values.

```yaml
profiles:
  nas-bmc:
    tables:
      - oid: .1.3.6.1.4.1.21317.1.3.1
        name_column: 13
        value_column: 2
        kind: temperature
        match: 'Temp$'
        exclude: '^(CPU2|Vcpu2VRM|VmemEFVRM|VmemGHVRM|P[12]-DIMM[C-H])'
      - oid: .1.3.6.1.4.1.21317.1.3.1
        name_column: 13
        value_column: 2
        kind: fan
        match: '^FAN'
        exclude: '^FAN(1|3|4|A|B)$'
targets:
  - id: nas-bmc
    name: NAS BMC
    host: 192.0.2.20
    profile: nas-bmc
    community_file: /run/secrets/nas-bmc-community
```

### Example for the CGW-Fiber

The CGW-Fiber gives its sensors in the LM-SENSORS-MIB tables. The value is in milli-degrees, so `scale` is 1000. The Profile also uses the built-in `ucd` sources.

The rows `temp3` and `fan2` always read 0, so `exclude` removes them.

```yaml
profiles:
  cgw-fiber:
    cpu: ucd
    memory: ucd
    tables:
      - oid: .1.3.6.1.4.1.2021.13.16.2.1
        name_column: 2
        value_column: 3
        kind: temperature
        scale: 1000
        exclude: '^temp3$'
      - oid: .1.3.6.1.4.1.2021.13.16.3.1
        name_column: 2
        value_column: 3
        kind: fan
        exclude: '^fan2$'
targets:
  - id: cgw-fiber
    name: CGW-Fiber
    host: 192.0.2.10
    profile: cgw-fiber
    community_file: /run/secrets/cgw-fiber-community
```

A test reads the same rules against the recorded files of the 2 Targets and checks the fans and the temperatures. No test parses the YAML text of these examples.

## Read timing and image

The hub sends `GetData` to each Target each 60 s. It also sends `GetData` each second while the "1 minute" chart of a system is open. The bridge reads a Target at a safe rate, whatever the hub asks.

### Rules of the read

The following list gives the 5 rules. The times are constants, with no config key.

1. The bridge makes one read at a time for each Target. A request that comes while a read runs waits for that read and gets its result. This also holds when the connection of the Target closes and opens again.
2. A read starts at most each 10 s. In that time, the bridge replies with the last result: the good result or the error.
3. One read has a deadline of 20 s. The hub waits 30 s. At the deadline the read fails, and the bridge drops a late reply of the device.
4. After a failed read, the bridge replies with an error and never with an old result.
5. After a failed read, the bridge does not connect that Target again until a read is good.

One SNMP request gets 3 s and 2 retries, so it takes at most 9 s. One lost packet then does not fail a rule. The bridge ignores `CacheTimeMs`.

A Target that gives no answer at all is a failed read. When only some rules answer, the rules that answer give data.

### Down status

A Target that does not answer shows in the hub as down, and the hub saves no new stats for it. The bridge reads such a Target again each 60 s. After the first good read, the bridge connects the Target, and the system shows as up again.

At the start, each Target connects one time with no read first. A Target with a wrong address thus shows as down at once.

A connection can close for a different reason, for example a hub restart. The bridge then connects again with a backoff and with no read first.

The hub log of a Target that is down shows an SSH error: `connection refused` on port 45876. The hub tries SSH after it closes the WebSocket. This error is not the cause.

The real reason is in the log of the bridge. The bridge writes one warning line when a read of a Target fails: `read failed`, with the Target and the error text.

A Target that fails again and again gives this line one time, until a read is good. Then the bridge writes one line: `read is good again`.

A table rule that fails also writes its own line, so a Target can give more than one line. The error text never holds the community.

### Image

The file `internal/dockerfile_snmpbridge` builds the bridge in a scratch image with the CA certificates. It has no volume. Build it from the root of the repo:

```sh
docker build -f internal/dockerfile_snmpbridge -t snmpbridge .
```

A docker host can use the git URL and the tag as the build context.

Run `scripts/check-snmpbridge-image.sh` to check the image. The script builds the image and runs it with no `SNMPBRIDGE_CONFIG`. It passes when the bridge stops with a non-zero exit code and names `SNMPBRIDGE_CONFIG`.

### Hub image

The file `internal/dockerfile_hub_ui` builds the hub of the fork. The upstream file `internal/dockerfile_hub` builds only the Go code, and it needs a `dist` folder that git does not hold.

The new file builds the UI first with bun. Then it embeds the UI in the hub.

Build it from the root of the repo:

```sh
docker build -f internal/dockerfile_hub_ui -t beszel-hub .
```

The hub and the bridge build from one tag of the fork. A docker host uses the same git URL and tag as the build context for both files:

| Image | File |
|---|---|
| Hub with the UI | `internal/dockerfile_hub_ui` |
| SNMP bridge | `internal/dockerfile_snmpbridge` |

Both files pin their base images with a version tag: `golang:1.27-alpine` and `oven/bun:1.4-alpine`. Step 7b of `scripts/sync-upstream.sh` stops the sync when `go.mod` needs a newer Go minor version than the pin. Then raise the Go pin in both files.

Run `scripts/check-hub-image.sh` to check the hub image. The script copies the files of the commit `HEAD` to a new folder, so the folder has no `dist` and no `node_modules`. It builds the image from that folder and starts a container.

Set `TREE=working` to check the working tree before a commit.

The script passes when `GET /` gives the HTML of the app, and one asset that the HTML names gives 200. It exits with a code that is not 0 when a step fails.

## Status of this version

This version loads and checks the config. The running bridge reads each Target with the SNMP reader, the rules of its Profile and the read timing. It reports the data of the Target, with CPU, memory and uptime.

A browser test ran the bridge against snmpsim and the stock hub 0.21.2. Both Targets showed with fans and temperatures. When snmpsim stopped, both systems showed as down in 42 s, and they were up again 62 s after the start.
