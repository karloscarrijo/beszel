# The SNMP bridge also reads an HTTP JSON API

Some network devices have no SNMP agent, and their controller gives the CPU and the memory only through an HTTP API that returns JSON. The first case is 3 UniFi Flex switches.

So the SNMP bridge gets a second source: a Target has a `host` for SNMP, or a `url` for an HTTP JSON API. A Profile names the JSON fields for CPU, memory and uptime, as it names the OID tables for SNMP. The code names no vendor. The URL and the field names of a vendor are only in the config file.

The HTTP source is in its own files. The bridge builds and passes its tests without these files. Why: an offer to upstream can then hold only the SNMP code. The upstream collaborator wrote on issue 1537 that SNMP keeps the feature generic, and upstream closed the request for UniFi metrics (issue 42) as out of scope.

The program keeps the name "SNMP bridge". A new name changes the image, the variable `SNMPBRIDGE_CONFIG` and each stack that runs the bridge.

## Considered options

- **A second program for one vendor**: the code names a vendor, and the protocol client has 2 users to keep in step with the hub.
- **An OpenMetrics source**: the upstream owner named this format in discussion 1671. It needs one more exporter container for each vendor.
- **Vendor code in the bridge**: upstream does not take vendor code, so the offer to upstream gets weaker.

## Consequences

- A device that has SNMP stays on SNMP. If the switches get an SNMP agent later, their Targets move to SNMP and the HTTP source has no user.
- The HTTP source reads only CPU, memory and uptime. It reads no fans and no temperatures.
