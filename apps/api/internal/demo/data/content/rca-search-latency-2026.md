# Search latency on api.northwind.example during the catalogue re-index

## Lazy reading
- **What broke:** `GET /search` p95 rose to 2.4 s on `api.northwind.example` for 50 minutes.
- **Why:** The nightly full re-index ran with 8 bulk threads against the live index (proven).
- **Impact:** Slow search for all customers between 02:10 and 03:00 UTC; no errors.
- **Fix:** Re-index into a new index and switch the alias (Option 1) — operator OK.
- **Status:** mitigated

## Root cause

`inventory-sync` triggers a full re-index of `products` every night. Since 10-12 the job writes into the live
index with 8 bulk threads, which competes with search queries on `prod-search-01` and `prod-search-02`.

## How to fix it

### Option 1: Blue/green re-index behind an alias (recommended)
- **Kind:** permanent fix
- **What:** write to `products-<date>`, switch the alias `products` when done, delete the old index
- **Why it works:** queries never hit an index under bulk load
- **Effort:** 1 day
- **Risk:** low
- **Blast radius:** the nightly job only
- **Approval:** operator OK

## Prompt for a write-access agent

~~~~markdown
# Blue/green re-index for products
## Goal
The nightly re-index never raises search p95 above 300 ms.
~~~~
