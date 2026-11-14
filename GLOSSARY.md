# Beszel fork

The terms of the work that this fork adds to Beszel. The terms of Beszel itself (hub, agent, system) keep their upstream meaning.

## Language

**SNMP bridge**:
A program that reads devices through SNMP and reports each one to the hub as a system.
_Avoid_: SNMP agent (in SNMP, the agent is the service on the device), poller, proxy

**Target**:
One device that the SNMP bridge reads.
_Avoid_: device, host, node

**Profile**:
A named set of sensor rules that tells the SNMP bridge what to read from a Target and how to turn each value into a fan speed, a temperature, a CPU use or a memory use. More than one Target can use the same Profile.
_Avoid_: template, MIB, device type
