# Beszel fork

The terms of the work that this fork adds to Beszel. The terms of Beszel itself (hub, agent, system) keep their upstream meaning.

## Language

**SNMP bridge**:
A program that reads devices through SNMP and reports each one to the hub as a system.
_Avoid_: SNMP agent (in SNMP, the agent is the service on the device), poller, proxy

**Target**:
One device that the SNMP bridge reads.
_Avoid_: device, host, node
