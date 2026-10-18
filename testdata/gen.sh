#!/usr/bin/env bash
# Generates media and subtitle fixtures that mimic a real library.
# Idempotent: existing files are skipped. Output: testdata/fixtures/ (git-ignored).
set -euo pipefail

FFMPEG=${FFMPEG:-/opt/homebrew/bin/ffmpeg}
FFPROBE=${FFPROBE:-/opt/homebrew/bin/ffprobe}
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
OUT="$ROOT/fixtures"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

ff() { "$FFMPEG" -hide_banner -nostdin -loglevel error -y "$@"; }

# make_media <out> <ffmpeg args without output>: encodes to a .part file and renames it,
# so an interrupted run never leaves a truncated fixture that a later run would skip.
make_media() {
  local out=$1; shift
  if [[ -e $out ]]; then echo "skip  ${out#"$OUT"/}"; return; fi
  mkdir -p "$(dirname "$out")"
  local part="${out%.*}.part.${out##*.}"
  ff "$@" "$part"
  mv "$part" "$out"
  echo "made  ${out#"$OUT"/}"
}

# put <src> <dst>: copies a text file into place unless it already exists.
put() {
  if [[ -e $2 ]]; then echo "skip  ${2#"$OUT"/}"; return; fi
  mkdir -p "$(dirname "$2")"
  cp "$1" "$2"
  echo "made  ${2#"$OUT"/}"
}

# ---- Subtitles (written by this script) -------------------------------------

cat > "$TMP/direct.en.srt" <<'EOF'
1
00:00:01,000 --> 00:00:04,000
Welcome to the Café.

2
00:00:05,000 --> 00:00:08,000
The projector is ready.

3
00:00:09,000 --> 00:00:12,000
Let's begin.
EOF

cat > "$TMP/direct.es.forced.utf8.srt" <<'EOF'
1
00:00:02,000 --> 00:00:05,000
¡Hola! ¿Qué tal, señor?

2
00:00:06,000 --> 00:00:09,000
Esto es necesario.

3
00:00:10,000 --> 00:00:13,000
Gracias.
EOF
# windows-1252 encoding of the forced Spanish track (the accents become single bytes)
iconv -f UTF-8 -t CP1252 "$TMP/direct.es.forced.utf8.srt" > "$TMP/direct.es.forced.srt"

cat > "$TMP/multi.eng.srt" <<'EOF'
1
00:00:01,000 --> 00:00:04,000
Embedded English subtitle one.

2
00:00:05,000 --> 00:00:08,000
Embedded English subtitle two.
EOF

cat > "$TMP/multi.spa.ass" <<'EOF'
[Script Info]
ScriptType: v4.00+
PlayResX: 640
PlayResY: 360

[V4+ Styles]
Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding
Style: Default,Arial,24,&H00FFFFFF,&H000000FF,&H00000000,&H64000000,0,0,0,0,100,100,0,0,1,2,0,2,10,10,10,1

[Events]
Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text
Dialogue: 0,0:00:01.00,0:00:04.00,Default,,0,0,0,,Subtítulo embebido en español.
Dialogue: 0,0:00:05.00,0:00:08.00,Default,,0,0,0,,Segundo subtítulo.
EOF

cat > "$TMP/sdh.eng.srt" <<'EOF'
1
00:00:01,000 --> 00:00:03,000
[door creaks open]

2
00:00:04,000 --> 00:00:06,000
- Who's there?
- [whispering] Nobody.

3
00:00:07,000 --> 00:00:09,000
[thunder rumbles]
EOF

# ---- Movies ------------------------------------------------------------------

D="$OUT/Movies/Direct.Movie.2020.1080p"
make_media "$D/Direct.Movie.2020.1080p.mp4" \
  -f lavfi -i "testsrc=d=20:s=1280x720:r=24" \
  -f lavfi -i "sine=d=20" \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -c:a aac -ac 2
put "$TMP/direct.en.srt" "$D/Direct.Movie.2020.1080p.en.srt"
put "$TMP/direct.es.forced.srt" "$D/Direct.Movie.2020.1080p.es.forced.srt"

make_media "$OUT/Movies/Hevc.Movie.2019/Hevc.Movie.2019.mkv" \
  -f lavfi -i "testsrc=d=20:s=640x360:r=24" \
  -f lavfi -i "sine=d=20" \
  -c:v libx265 -preset ultrafast -pix_fmt yuv420p -x265-params log-level=error \
  -c:a aac -ac 2

make_media "$OUT/Movies/Multi.Audio.2018/Multi.Audio.2018.mkv" \
  -f lavfi -i "testsrc=d=20:s=640x360:r=24" \
  -f lavfi -i "sine=f=440:d=20" \
  -f lavfi -i "sine=f=880:d=20" \
  -i "$TMP/multi.eng.srt" \
  -i "$TMP/multi.spa.ass" \
  -map 0:v -map 1:a -map 2:a -map 3:s -map 4:s \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -c:a aac -ac 2 -c:s copy \
  -metadata:s:a:0 language=eng -metadata:s:a:1 language=spa \
  -metadata:s:s:0 language=eng -metadata:s:s:1 language=spa

make_media "$OUT/Movies/Sample.2021/sample.mkv" \
  -f lavfi -i "testsrc=d=5:s=640x360:r=24" \
  -f lavfi -i "sine=d=5" \
  -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
  -c:a aac -ac 2

# ---- Shows -------------------------------------------------------------------

SHOW="$OUT/Shows/Test Show/Season 1"
for ep in 1 2; do
  make_media "$SHOW/Test.Show.S01E0$ep.mkv" \
    -f lavfi -i "testsrc=d=10:s=640x360:r=24" \
    -f lavfi -i "sine=d=10" \
    -c:v libx264 -preset ultrafast -pix_fmt yuv420p \
    -c:a aac -ac 2
done
put "$TMP/sdh.eng.srt" "$SHOW/Test.Show.S01E01.SDH.eng.srt"

# ---- Verification ------------------------------------------------------------

# probe_streams <file>: one normalized "type,codec[,language]" line per stream, sorted.
# ffprobe prints codec_name before codec_type; undefined languages ("und") are dropped.
probe_streams() {
  "$FFPROBE" -v error -show_entries stream=codec_name,codec_type:stream_tags=language \
    -of csv=p=0 "$1" | awk -F, '{ lang = ($3 == "" || $3 == "und") ? "" : "," $3; print $2 "," $1 lang }' | sort
}

# verify <file> <expected stream lines...>: exact match of streams, and yuv420p video.
verify() {
  local f=$1; shift
  local want got pix
  want=$(printf '%s\n' "$@" | sort)
  got=$(probe_streams "$f")
  if [[ $want != "$got" ]]; then
    echo "FAIL: ${f#"$OUT"/}" >&2
    echo "  expected:" >&2; printf '    %s\n' "$@" >&2
    echo "  found:" >&2; printf '    %s\n' $got >&2
    exit 1
  fi
  pix=$("$FFPROBE" -v error -select_streams v -show_entries stream=pix_fmt -of csv=p=0 "$f")
  if [[ $pix != yuv420p ]]; then
    echo "FAIL: ${f#"$OUT"/} video pix_fmt is '$pix', expected yuv420p" >&2
    exit 1
  fi
  echo "ok    ${f#"$OUT"/}"
}

verify "$D/Direct.Movie.2020.1080p.mp4" "video,h264" "audio,aac"
dims=$("$FFPROBE" -v error -select_streams v:0 -show_entries stream=width,height -of csv=p=0 "$D/Direct.Movie.2020.1080p.mp4")
[[ $dims == 1280,720 ]] || { echo "FAIL: Direct movie is $dims, expected 1280,720" >&2; exit 1; }

verify "$OUT/Movies/Hevc.Movie.2019/Hevc.Movie.2019.mkv" "video,hevc" "audio,aac"
verify "$OUT/Movies/Multi.Audio.2018/Multi.Audio.2018.mkv" \
  "video,h264" "audio,aac,eng" "audio,aac,spa" "subtitle,subrip,eng" "subtitle,ass,spa"
verify "$OUT/Movies/Sample.2021/sample.mkv" "video,h264" "audio,aac"
verify "$SHOW/Test.Show.S01E01.mkv" "video,h264" "audio,aac"
verify "$SHOW/Test.Show.S01E02.mkv" "video,h264" "audio,aac"

echo "all fixtures verified in $OUT"
