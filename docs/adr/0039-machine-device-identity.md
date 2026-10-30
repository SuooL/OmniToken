# ADR-0039 Stable machine identity across agent installations

Status: accepted (2026-10-29).

Device IDs must survive deleting agent.json and reinstalling the agent. Display
names and credentials are independent of machine identity. Hostnames, users,
network interfaces and installation paths are not identity inputs.

Use an application-namespaced SHA-256 digest of the platform machine identifier,
encoded as a version-8 UUID. Windows uses SMBIOS System UUID, macOS uses
IOPlatformUUID, Linux uses /etc/machine-id. Raw identifiers never leave the agent.
Linux OS reinstallation can change machine-id; this design guarantees agent
reinstallation, not replacement of hardware or OS identity. Missing or invalid
machine identifiers fail enrollment rather than silently generating another UUID.
Cloned systems must regenerate their system identity before enrolling; machine
identity is not proof of possession and never replaces admin authentication.

Existing random device IDs remain canonical. Admin enrollment binds the machine
key to that existing ID. Later enrollment from an empty config looks up the same
binding and returns the canonical ID, rotating the credential if needed. The
binding is unique in both directions. Conflicting existing bindings and revoked
principals fail rather than silently reassigning identity or undoing revocation.
Legacy clients retain the existing enrollment behavior. Upgrade the Hub first.

Existing duplicate histories require a confirmed ADR-0019 merge once. Fleet
views omit revoked registry principals covered by that merge audit; credentials,
heartbeats and ingest receipts remain unchanged. No names-based merging occurs.

Validation: repeat fresh enrollment on one machine; preserve old random ID on
upgrade and config deletion; credential rotation invalidates the old token;
reject conflicting bindings and revoked identities; merged fleet has one row.
