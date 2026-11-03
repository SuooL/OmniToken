# Desktop universal updater repair

- Confirm the published 0.4.2 archive fails the ARM64 + x86_64 architecture check before changing the workflow.
- Install both Rust targets, build a universal app, validate both slices and strict resource signing, and use the universal output path throughout publication.
- Run make check; rebuild and sign the 0.4.2 archive from the exact tagged application source, verify the updater signature and matching manifest, then replace its release assets.
- Preserve the existing Hub deployment and installed application behavior. Merge the workflow repair through a coauthored PR to dev and verify remote CI.
