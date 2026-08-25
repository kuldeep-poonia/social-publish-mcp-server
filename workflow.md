# WORKFLOW.md — Recording & GitHub Process

This governs how the build is recorded for YouTube and how it is pushed to GitHub. This is a process document, separate from PRODUCT.md and BUILD_PLAN.md.

## 1. Video Recording Rules

- One dedicated top-level folder for videos, named by project (not by "phase" wording — see commit rules below for why "phase" language stays out of anything human-facing).
- Inside it, one subfolder per file being built. Subfolder name = the file's name (e.g. `token_encryption_go/`).
- Each subfolder contains the video(s) covering that file, in the order they were recorded, so they can be dropped into a single edited sequence later without guesswork on ordering.
- If a file is created and later modified (bug fix, refactor, added function), the modification is recorded as an additional video inside that same file's subfolder, appended after the original — not as a separate top-level entry. This keeps the full lifecycle of one file together for editing.
- **Test files are not recorded during creation** — writing a `_test.go` file itself is not filmed.
- **Test execution IS recorded** — running the test suite and watching real values (latency numbers, pass/fail counts, load test results) come back is filmed, since that's the payoff moment for viewers.
- Explain out loud while writing, the way a human engineer walks through their own reasoning — not narrating "now I will hallucinate-check this."

## 2. Privacy Rule for Videos (Non-Negotiable)

Before any video is finalized/uploaded, verify none of the following are visible on screen at any point:
- Real API keys, tokens, client secrets, or `.env` contents
- Real database credentials or connection strings
- Any real user data if testing is later done against a real account
- Personal email addresses, phone numbers, or other identifying info not meant for the video

Use placeholder/dummy values on screen wherever a real secret would normally appear. If a real secret was ever visible during a take, that segment is re-recorded or cut before upload — it does not go out "just this once."

## 3. GitHub Repository Rules

- Repository is created before the first file is written.
- Repo includes a full description (what the project is, one paragraph) and relevant topic tags (e.g. `mcp`, `golang`, `oauth2`, `social-media-api`, `multi-tenant`).
- `.gitignore` is committed first, before any other file, covering: `.env`, `*.pem`, `*.key`, `/secrets`, build artifacts, and the video folder (raw video files do not belong in a code repository — keep them local/on a separate storage, not in git history).
- **The 4 planning documents themselves (`PRODUCT.md`, `BUILD_PLAN.md`, `GEMINI_INSTRUCTIONS.md`, `WORKFLOW.md`) are also gitignored.** They stay local/private — internal strategy, security-test targets, and the exact instructions given to the coding agent are not exposed in the public repo. Keep them in a folder (e.g. `/docs-internal/`) and add that folder to `.gitignore`.

## 4. Commit Rules

- **A commit happens as soon as a file is created or meaningfully changed** — not batched at the end of a session.
- **Commit messages are written in plain human language describing what changed and why**, not internal phase/step labeling.
  - Good: `Add AES-GCM encryption wrapper for OAuth tokens at rest`
  - Bad: `Phase 1 Step 3 complete`
- No secrets, tokens, or credentials in any commit, ever — this is checked before push (see GEMINI_INSTRUCTIONS.md security rules and the pre-commit secret scan from BUILD_PLAN.md Phase 0).
- Test files are committed like any other file, following the same immediate-commit rule.

## 5. Sequencing Note

Recording and pushing follow the same order as BUILD_PLAN.md's phases — but phase numbers are an internal planning tool only. They should not leak into commit messages, video titles, or the public-facing repo description. The public story is "building a product," not "executing phase 4."