---
name: tag-container-release
description: Tag and push a new Clock Relay GitHub release tag that triggers the container publishing workflow. Use when the user asks to create, tag, push, build, or publish a new container release for this project, including requests with an explicit version like v0.0.3 or requests to determine the next version from existing tags.
---

# Tag Container Release

Use this workflow to create an annotated Git tag for a Clock Relay container release. Pushing a `v<major>.<minor>.<patch>` tag triggers `.github/workflows/container.yml`, which tests the repo, publishes a multi-architecture GHCR image, and creates or updates the matching GitHub Release.

## Version Selection

Accept an explicit version from the user only if it matches `v<major>.<minor>.<patch>`, for example `v0.0.3`.

When the user does not provide a version:

1. Fetch tags so local version detection is current:
   ```bash
   git fetch --tags --prune-tags origin
   ```
2. List existing version tags:
   ```bash
   git tag -l 'v*' --sort=-v:refname
   ```
3. Choose the latest valid semantic version tag matching `^v[0-9]+\.[0-9]+\.[0-9]+$`.
4. Default to bumping the patch version, for example `v0.0.2` to `v0.0.3`.
5. If there are no version tags, propose `v0.0.1`.

Before accepting the default patch bump, inspect the unreleased work and make an opinionated release recommendation:

```bash
git log --reverse --oneline <previous-tag>..HEAD
git diff --stat <previous-tag>..HEAD
```

Suggest a minor or major bump instead of a patch when the commits or diff indicate a larger release. Treat these as signals:

- Breaking change: removed or incompatible CLI flags, config fields, HTTP routes, API behavior, database/store behavior, container runtime contract, image layout, deployment requirements, or user-facing workflow.
- Minor change: new feature, new endpoint, new config capability, new command mode, significant UI addition, or behavior that users can opt into.
- Patch change: bug fix, test-only change, docs-only change, internal refactor, small UI polish, or dependency/build maintenance with no user-facing contract change.

For `0.x` releases, prefer a minor bump for breaking or substantial feature work unless the user explicitly wants a major version. If the user provided a patch version but the work looks breaking or feature-sized, pause and recommend the better version before creating a tag.

Ask the user to confirm the proposed version before creating the tag unless they already gave an explicit version and the release size looks consistent with it.

## Release Steps

1. Verify the worktree and branch context:
   ```bash
   git status --short
   git branch --show-current
   git rev-parse HEAD
   ```
   If there are uncommitted changes, explain that the tag will point at the current committed `HEAD`, not the dirty worktree. Ask whether to continue.

2. Determine the previous release tag with:
   ```bash
   git tag -l 'v*' --sort=-v:refname
   ```

3. Check whether the requested tag already exists locally or remotely:
   ```bash
   git rev-parse -q --verify "refs/tags/<version>"
   git ls-remote --tags origin "refs/tags/<version>"
   ```
   If either command finds the tag, stop. Never force-push or overwrite release tags.

4. Collect commits since the previous tag in chronological order:
   ```bash
   git log --reverse --oneline <previous-tag>..HEAD
   ```
   If there is no previous tag, use:
   ```bash
   git log --reverse --oneline HEAD
   ```

5. If there are no commits since the previous tag, stop and tell the user there is nothing to release.

6. Create release notes from the commit summaries. Preserve PR numbers already present in commit messages, such as `(#123)`.

7. Create an annotated tag:
   ```bash
   git tag -a <version> -m "Release <version>

   Changes since <previous-tag>:

   - <commit summary> (#PR)
   - ...
   "
   ```
   If this is the first release, use `Changes in this release:` instead of `Changes since <previous-tag>:`.

8. Show the tag contents:
   ```bash
   git tag -n99 <version>
   ```

9. Ask the user to confirm before pushing. If they reject the release after the local tag was created, ask whether to delete the local tag with `git tag -d <version>`.

10. Push the tag:
    ```bash
    git push origin <version>
    ```

11. Report the pushed tag and the image that the GitHub Actions container workflow will publish:
    ```text
    ghcr.io/johnnycon/clock-relay:<version-without-v>
    ```
    The workflow builds and publishes `linux/amd64` and `linux/arm64` images for tag pushes matching `v*.*.*`. It also publishes or refreshes `latest` for quick local trials, but downstream deployment docs should use the exact version tag.

12. Provide the canonical discovery links in the final response:
    ```text
    GitHub Release: https://github.com/Johnnycon/clock-relay/releases/tag/<version>
    Container package: https://github.com/Johnnycon/clock-relay/pkgs/container/clock-relay
    Exact image: ghcr.io/johnnycon/clock-relay:<version-without-v>
    ```

13. When the workflow has had time to run, verify the public surfaces if the user asked you to wait:
    ```bash
    gh run list --workflow Container --event push --limit 5
    gh release view <version> --repo Johnnycon/clock-relay
    docker manifest inspect ghcr.io/johnnycon/clock-relay:<version-without-v>
    ```
    If checking from public web pages instead of `gh`, verify the GitHub Release and GHCR package both show the exact image tag.

## Rules

- Use only tags formatted as `v<major>.<minor>.<patch>`.
- Use annotated tags with `git tag -a`; never use lightweight tags.
- Never force-push, delete, move, or overwrite a remote tag.
- List release-note commits in chronological order, oldest first.
- Include PR numbers when they are already present in commit messages.
- Do not push until the user confirms after seeing the final tag contents.
- Treat `ghcr.io/johnnycon/clock-relay:<version-without-v>` as the canonical deployment image. Treat `latest` as a convenience tag only.
