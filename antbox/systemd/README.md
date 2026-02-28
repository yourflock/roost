# antbox/systemd

## Purpose

Service manager definitions for automatic startup and recovery on edge devices.

## Expected Contents

- implementation code for this domain
- tests and fixtures relevant to this domain
- local configuration templates where required
- domain-specific docs updates when behavior changes

## Integration Contracts

- align with top-level app README and docs
- consume shared contracts from backend where relevant
- maintain clear boundaries to avoid cross-domain coupling

## Next Implementation Steps

Add hardened unit files with watchdog and restart policies.
