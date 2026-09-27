# Organizations in the core

Status: accepted, 2026-09-27

## Context

kmdn was specified as single-tenant: one instance is one organization ([D15](../specs/decisions.md)). A hosted service needs one deployment to serve many organizations. Running one kmdn instance per customer, or many per process, keeps the core unchanged but multiplies databases, processes and upgrades. A shared schema serves every organization from one database and allows one account to belong to several organizations, but touches most queries, which a private fork would then have to carry forever.

## Decision

We will make organizations a core concept ([spec 16](../specs/16-organizations.md), [D61](../specs/decisions.md)). Org-owned rows carry `org_id`; accounts are global and join orgs through memberships; the org appears in URLs (`/{org}/{owner}/{repo}`, `/api/v1/orgs/{org}/…`). Self-hosted installs run in `single` mode with one default org, so nothing changes for them. Plans, billing and other hosted-service concerns stay out of the core and plug in through a public embedding package (`pkg/kmdn`) with policy, event, sign-in and provisioning hooks.

Isolation is enforced in the service layer, proven by a cross-org test over every API operation, and backed by Postgres row-level security.

## Consequences

Every org-scoped query and every ID-addressed resource must check the org; the cross-org test is the guard. API paths for collections move under `/orgs/{org}`, the SPA gains an org segment with redirects from old URLs, and the admin console splits into org and instance consoles. Upload deduplication becomes per org. The instance-wide AI budget becomes per org. Horizontal scaling stays out of scope: one node still serves an instance.
