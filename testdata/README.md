# testdata

Generated media and subtitle fixtures for the library scanner tests.

Regenerate (idempotent, skips files that already exist):

    ./testdata/gen.sh

Output goes to `testdata/fixtures/` (git-ignored). Needs ffmpeg and ffprobe at
`/opt/homebrew/bin` (override with `FFMPEG=` and `FFPROBE=`). The script exits
non-zero if any generated file is missing its expected streams.
`testdata/golden/` is separate and is not written by the generator.
