# Security Policy

[简体中文](./SECURITY.md)

Kiln is a gateway that serves the network. It holds password hashes, session signing keys, admin API tokens and play keys, it decrypts upstream media when configured to, and it makes outbound requests on the operator's behalf. A defect in any of those places lands on whoever is running it, so please use the process below.

## Supported versions

| Version | Status |
| --- | --- |
| 1.0.x | Receiving security reports |

Only the latest patch release receives fixes. Please upgrade and confirm the issue still reproduces before reporting.

## How to report

**Do not report security problems in a public issue.** A public issue is visible to everyone, which publishes an exploitable defect before a patch exists.

Open a private security advisory instead: [report a vulnerability](https://github.com/babywbx/Kiln/security/advisories/new). Only the maintainer can see it, and both the discussion and the patch stay on a private fork until the advisory is published.

A report that can be acted on immediately usually includes:

- The affected version and variant, meaning Lite, Core or Full
- The deployment shape: binary, Docker or Windows service, and whether the service faces the public internet
- The relevant configuration, with passwords, tokens, play keys and decryption keys removed first
- Reproduction steps, ideally a minimal request or a minimal config
- The impact as you see it, for example authentication bypass, arbitrary file read, or unauthorized access to the admin API

## What to expect

This project has one maintainer. The following are best effort targets, not a service level agreement:

| Stage | Target |
| --- | --- |
| Acknowledgement | Within 5 working days |
| Initial triage and severity assessment | Within 10 working days |
| Fix and release | Depends on complexity, high severity first |

If a target passes with no reply, add a comment on the advisory as a reminder.

## Scope

These are security issues:

- Bypassing login, session validation, admin API tokens or play keys
- Play key privilege escalation, such as reaching channels outside the key's scope
- Path traversal, arbitrary file read or write, or reaching content outside `data_dir`
- Server side request forgery through the egress proxy or a channel source, including cloud metadata endpoints and private network addresses
- Disclosure of decryption keys, the session signing key, password hashes or token plaintext
- Cross site scripting or cross site request forgery in the admin console
- Crashes, unbounded memory growth or denial of service triggered by remote input

These are not handled as security issues, please open a normal issue:

- Consequences of exposing the admin interface or pprof directly to the public internet
- Weak passwords, reused tokens, or configuration files left world readable
- Problems in an upstream site itself, or playback failures caused by what an upstream returns
- Issues that require local root, or write access to `data_dir` that the attacker already holds
- Feature requests for additional hardening, such as finer grained rate limit dimensions
- Published vulnerabilities in dependencies along code paths Kiln never reaches; open an issue to discuss the upgrade

## Disclosure

Coordinated disclosure. Once a patch ships, a public advisory describes the affected range and the fixed version. Unless you ask to stay anonymous, the advisory credits you by name or GitHub handle. There is no bug bounty.

## Hardening

Before deploying, read [Authentication and credentials](https://kiln.wbxdocs.com/en/guide/auth/) and [Operations](https://kiln.wbxdocs.com/en/guide/operations/) on the documentation site.
