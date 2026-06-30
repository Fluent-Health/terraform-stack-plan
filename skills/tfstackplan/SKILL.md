---
name: tfstackplan
description: "Use when linting, planning, refactoring state, verifying, or applying multi-stack Terraform infrastructure in this repository."
---

# tfstackplan Agent Skill

This skill governs the complete workflow for executing and verifying Terraform infrastructure changes across stacks in this repository. You MUST load and follow this skill when planning, applying, or refactoring Terraform state.

## Core Workflows

### 1. Code Validation & Linting
Before planning or submitting changes, ensure configuration and syntax are completely clean.
* **Command:**
  ```bash
  tfstackplan run lint --dir .
  ```

### 2. Planning Infrastructure Changes
To generate plans across changed stacks in your branch or PR workspace:
* **Command (Only changed stacks):**
  ```bash
  tfstackplan run plan --dir . --changed
  ```
* **Command (All stacks):**
  ```bash
  tfstackplan run plan --dir . --changed=false
  ```
* **Notes:** This generates per-stack `tfplan.json` files and renders a markdown plan report in the workspace.

### 3. State Migrations & Resource Refactoring
When restructuring modules or resource addresses, use declarative shims to avoid physical resource destruction and recreates.

#### Same-Stack Relocations
When moving a resource or module within the SAME stack:
* **Command:**
  ```bash
  tfstackplan state move --dir . --stack <stack-path> <from-addr> <to-addr>
  ```
* **Requirement:** Run `run plan` first; same-stack moves are validated against plan files at generation.

#### Cross-Stack Relocations
When migrating resources between DIFFERENT stacks:
* **Command (Natively uses import/removed shims):**
  ```bash
  tfstackplan state move --dir . <source-stack>:<from-addr> <dest-stack>:<to-addr>
  ```

#### External Resource Imports
To adopt existing infrastructure into Terraform under a specific stack:
* **Command:**
  ```bash
  tfstackplan state import --dir . --stack <stack-path> <to-addr> <provider-id>
  ```

#### Standalone Resource Removals
To drop a resource from state without destroying the underlying physical infrastructure:
* **Command:**
  ```bash
  tfstackplan state remove --dir . --stack <stack-path> <addr>
  ```

### 4. Monitoring & Verifying CI/CD Executions
During pull requests or branch runs, you can programmatically fetch or stream the real-time execution status from the central server.
* **Get Status Once:**
  ```bash
  tfstackplan run status <execution-id> --format json
  ```
* **Watch live progress in the terminal:**
  ```bash
  tfstackplan run status <execution-id> --watch
  ```

### 5. Managing Apply Locks (Claims)
To avoid concurrent apply races, tfstackplan manages lease claims on individual stacks.
* **List current active claims:**
  ```bash
  tfstackplan run claims list
  ```
* **Force-release a stuck lease (e.g. from an aborted run):**
  ```bash
  tfstackplan run claims release --stack <stack-path> --pr <pr-number>
  ```

### 6. Applying Infrastructure Changes
To execute approved and verified plans against live infrastructure:
* **Command:**
  ```bash
  tfstackplan run apply --dir . --execute
  ```
* **Notes:** Pre-runs pending cross-stack state moves before executing the native terraform apply steps.

## Maintenance & Housekeeping

When wrapping up a feature branch or PR, always clean up the local temporary state files:
* **Listing active shims:** `tfstackplan state list`
* **Cleaning up after merge/apply:** `tfstackplan state cleanup --pr <pr-number>`
