# Releasing

How we cut a release of the images in this repository. It's short on purpose:
we're early, and this will change as we learn what we need.

> **Status:** the release workflow is still in review
> ([#11](https://github.com/agentexecutor/images/pull/11)). Until it merges,
> pushing a tag publishes nothing. The steps below describe how a release works
> once it has landed.

## Where we are

- **There is no release cadence.** We cut a release when somebody needs one.
- **We're targeting the hero demos for now.** A release exists so the AX hero
  demo (`hack/hero-demo` in [google/ax](https://github.com/google/ax)) can run
  on a published image pinned by digest. A release is good enough when the hero
  demo works on it.
- **There is no compatibility or support policy yet.** That comes later
  ([#17](https://github.com/agentexecutor/images/issues/17)).

## What a release publishes

Pushing a `vMAJOR.MINOR.PATCH[-PRERELEASE]` tag runs the **Release Images**
workflow. It builds each image for linux/amd64 and linux/arm64 and pushes it to
`ghcr.io/agentexecutor/<image>:<tag>`.

- **Published:** `antigravity` and `mock-gemini-api`, the fake Gemini API server
  used for testing (built from `fakes/mock-gemini`).
- **Not published:** anything for Anthropic. We aren't doing Anthropic at all
  to start with, so `claude-code` and the `mock-anthropic` test server are
  left out.
- The published `antigravity` image doesn't contain the `agy` binary for now
  ([#15](https://github.com/agentexecutor/images/issues/15)). A task installs
  it when it starts; see the
  [Antigravity image README](../agents/antigravity/README.md).
- `:latest` is never moved. Reference images by digest.
- Rerunning the workflow for a tag doesn't rebuild images that already exist,
  so the digests of a release don't change.

## Cutting a release

Only repository admins can push a tag (see [rulesets](rulesets.md)).

1. Pick a commit on `main` whose checks are green.
2. Pick a version. If you aren't sure the release is good, start with a
   prerelease such as `v0.0.0-rc.1`.

   Check the name and the commit before you push. Once a tag exists, nobody can
   move or delete it, so a mistake costs a version number.
3. Tag and push:

   ```bash
   git fetch origin
   git tag v0.0.0-rc.1 origin/main
   git push origin v0.0.0-rc.1
   ```

4. Watch the **Release Images** run under **Actions**. Its summary lists the
   digest of every image it published.
5. If an image was published for the first time, make its package public (see
   below).
6. Run the hero demo against the published digest. If a prerelease holds up,
   tag the stable version on the same commit.

## Making a new package public

ghcr.io creates a new package as private, so the first release of an image
can't be pulled anonymously until an organization owner changes that:

1. Open the package from the
   [organization's packages](https://github.com/orgs/agentexecutor/packages).
2. In **Package settings**, change the visibility to **Public**.
3. In the same settings, link the package to this repository.

Then check that it pulls without credentials:

```bash
docker logout ghcr.io
docker pull ghcr.io/agentexecutor/<image>@sha256:<digest>
```

## When something goes wrong

- **The workflow failed partway.** Rerun it. Images that were already pushed
  are skipped, so their digests stay the same.
- **The release is bad.** The tag can't be moved, so fix the problem on `main`
  and cut the next version.
