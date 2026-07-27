# 01 - Overview

## Problem statement

[Bifrost](https://docs.getbifrost.ai/overview) is an AI gateway whose access is controlled by **virtual keys** - governance entities bundling budgets, rate limits, per-provider/model scope, and expiry. Today these keys are created via Bifrost's dashboard or management API and distributed by hand, which brings the usual long-lived-credential problems:

- Keys are copied into app configs and CI systems and rarely rotated.
- No central lease or expiry is tied to the consuming workload's identity.
- Revocation is a manual, error-prone step when a service is decommissioned or compromised.
- Auditing "who holds which Bifrost key, and why" is hard.

Vault solves exactly this for databases, cloud IAM, and PKI via **dynamic secrets**: a client authenticates, asks for a credential, receives a short-lived one, and Vault revokes it when the lease ends.

**This project brings Bifrost credentials under that model** - a Vault secrets engine that provisions, leases, and revokes them on demand.

## Which direction? (read this first)

There are two plausible "Vault + Bifrost" integrations, and they are opposites. This is the second.

1. **Bifrost reads secrets from Vault** - *already exists natively.* Bifrost's enterprise [secret management](https://docs.getbifrost.ai/enterprise/secret-management) lets any secret field use a `vault.<path>` reference, resolved from a HashiCorp Vault (KV v2) at runtime, with hourly refresh and a `POST /api/vault/flush-cache` endpoint. Here **Bifrost is the Vault client**. We are **not** rebuilding this.

2. **Vault manages Bifrost credentials** - *this project.* A Vault secrets engine calls Bifrost's **management API** to create/revoke/rotate virtual keys (and later provider keys). Here **Vault is the Bifrost client**, and Vault's lease system governs each credential's lifetime.

A secrets engine - not a Bifrost Go plugin or a Vault auth method - is the right vehicle: it makes Vault the control plane that leases and revokes Bifrost credentials, without running inside Bifrost or duplicating its native `vault.<path>` consumption.

## Goals

- **Dynamic Bifrost virtual keys.** `vault read bifrost/creds/<role>` returns a freshly-created, role-scoped virtual key bound to a Vault lease.
- **Automatic revocation.** When the lease expires or is revoked, the virtual key is deleted in Bifrost.
- **Role-based scoping.** Operators define roles that template the virtual key's providers, allowed models, budgets, rate limits, and TTLs.
- **Safe management-token handling.** The Bifrost management (bearer) token is stored in `config`, rotated automatically on a user-configurable schedule, and rotatable on demand - all without re-provisioning issued keys.
- **Operator familiarity.** Path layout and semantics mirror Vault's `database`/`aws` secrets engines.

## Non-goals (initially)

- Re-implementing Bifrost's `vault.<path>` **consumption** backend (direction 1 above).
- Managing Bifrost RBAC users, teams, MCP configs, or guardrails.
- A Vault **auth method** (exchanging a Bifrost key for a Vault token). Out of scope.
- Provider API key management - planned for **phase 2**, designed alongside but not part of the MVP.

## Personas

- **Platform / Vault operator** - enables the engine, sets `config` (Bifrost address + management token), and defines roles. Wants least-privilege and auditability.
- **Application / workload** - authenticates to Vault with its own identity and reads `creds/<role>` to obtain a short-lived Bifrost virtual key. Never sees the management token.
- **Security / compliance** - wants every Bifrost credential leased, attributable, and automatically revoked.

## Success criteria

- An app can obtain a working `sk-bf-…` virtual key from Vault, call Bifrost with it, and have it stop working after the lease ends - with no human in the loop.
- Rotating the Bifrost management token - manually or on the configured automatic schedule - does not disrupt already-issued leases.
- The design maps cleanly onto the Vault SDK and Bifrost's documented management API with no undocumented assumptions.
