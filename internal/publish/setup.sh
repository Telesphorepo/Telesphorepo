#!/bin/sh
# Telesphorepo project setup. Run it in the folder of a project (@@PROJECTS@@):
#
#   curl -fsSL @@URL@@/setup.sh | sh -s -- REPO [--ci none|github|jenkins-pipeline|jenkins-freestyle] [--yes]
#
# It sets up the project to publish to the repository REPO on @@URL@@. It changes only files of
# the project, never ~/.m2 or ~/.gradle, and it writes no secret. A second run changes nothing.
set -eu

URL='@@URL@@'
CREDENTIAL='@@CREDENTIAL@@'
JENKINS_NOTE='@@JENKINS_NOTE@@'
TOOLS='@@TOOLS@@'
NL='
'
CR=$(printf '\r')

say() { printf '%s\n' "$*"; }
die() {
	printf 'telesphorepo setup: %s\n' "$*" >&2
	exit 1
}

usage() {
	cat <<EOF
Set up this project to publish to $URL.

Examples:
  curl -fsSL $URL/setup.sh | sh -s -- releases --ci none
  curl -fsSL $URL/setup.sh | sh -s -- releases --ci github
  curl -fsSL $URL/setup.sh | sh -s -- releases --ci jenkins-pipeline --yes

  REPO        the repository to publish to
  --ci WHERE  where you publish from besides your computer: none, github,
              jenkins-pipeline or jenkins-freestyle
  --yes       change files of the project without a question

Run it in the folder with @@PROJECTS@@.
EOF
}

REPO=
CI=
CI_SET=
YES=
while [ $# -gt 0 ]; do
	case $1 in
	--ci)
		[ $# -ge 2 ] || die "--ci needs a value: none, github, jenkins-pipeline or jenkins-freestyle"
		CI=$2 CI_SET=1
		shift 2
		;;
	--ci=*)
		CI=${1#--ci=} CI_SET=1
		shift
		;;
	--yes | -y)
		YES=1
		shift
		;;
	-h | --help)
		usage
		exit 0
		;;
	-*) die "unknown option $1 (see --help)" ;;
	*)
		[ -z "$REPO" ] || die "name one repository, not \"$REPO\" and \"$1\""
		REPO=$1
		shift
		;;
	esac
done

# The questions read from the terminal: stdin is the script itself when it comes through a pipe.
TTY=
if (: </dev/tty) 2>/dev/null; then
	TTY=1
fi
ask() {
	printf '%s' "$1" >/dev/tty
	IFS= read -r ANSWER </dev/tty || ANSWER=
}

# ---------------------------------------------------------------- staged changes

# Each change goes to a working copy of the file first. Nothing in the project changes until the
# end, after the person agrees.
TMP=$(mktemp -d 2>/dev/null || mktemp -d -t telesphorepo)
trap 'rm -rf "$TMP"' EXIT
mkdir "$TMP/w"
: >"$TMP/files"
: >"$TMP/plan"
: >"$TMP/manual"
: >"$TMP/notes"

# stage_path FILE prints the path of the working copy of FILE.
stage_path() { printf '%s/w/%s' "$TMP" "$(printf '%s' "$1" | tr '/' '_')"; }

# stage_file FILE starts the working copy of FILE: a copy, or an empty file when FILE does not
# exist.
stage_file() {
	_w=$(stage_path "$1")
	if [ ! -e "$_w" ]; then
		if [ -f "$1" ]; then cp "$1" "$_w"; else : >"$_w"; fi
		printf '%s\n' "$1" >>"$TMP/files"
	fi
}

# plan FILE WHAT TEXT records a change to show before the files change.
plan() {
	printf '%s: %s\n' "$1" "$2" >>"$TMP/plan"
	printf '%s\n' "$3" | sed 's/^/    + /' >>"$TMP/plan"
}

# manual FILE WHY TEXT records a block that the person pastes by hand.
manual() {
	printf '\n%s: %s\n\n' "$1" "$2" >>"$TMP/manual"
	printf '%s\n' "$3" | sed 's/^/    /' >>"$TMP/manual"
}

note() { printf '%s\n' "$*" >>"$TMP/notes"; }

# eol FILE prints a carriage return when FILE has Windows line ends.
eol() { if grep -q "$CR\$" "$1" 2>/dev/null; then printf '%s' "$CR"; fi; }

# stage_insert_after FILE N TEXT puts the lines of TEXT after line N of the working copy of FILE
# (0: before the first line). At N = -1 it adds them at the end.
stage_insert_after() {
	_w=$(stage_path "$1")
	awk -v n="$2" -v text="$3" -v eol="$(eol "$_w")" '
		function emit(   i, k, parts) { k = split(text, parts, "\n"); for (i = 1; i <= k; i++) printf "%s%s\n", parts[i], eol }
		n == 0 && NR == 1 { emit(); done = 1 }
		{ print }
		NR == n { emit(); done = 1 }
		END { if (!done) emit() }' "$_w" >"$_w.new"
	mv "$_w.new" "$_w"
}

# stage_append FILE TEXT adds the lines of TEXT at the end of the working copy of FILE.
stage_append() { stage_insert_after "$1" -1 "$2"; }

# block_start FILE NAME prints the line of the top-level block "NAME {", or 0.
block_start() {
	awk -v name="$2" 'BEGIN { re = "^" name "[ \t]*[{]" } $0 ~ re { print NR; found = 1; exit } END { if (!found) print 0 }' "$1"
}

# block_end FILE NAME prints the line that closes the top-level block "NAME {": 0 when there is
# none, -1 when its braces do not close.
block_end() {
	awk -v name="$2" '
		BEGIN { re = "^" name "[ \t]*[{]"; end = 0 }
		!seen && $0 ~ re { seen = 1; open = 1; depth = 0 }
		open { l = $0; depth += gsub(/[{]/, "", l); l = $0; depth -= gsub(/[}]/, "", l); if (depth <= 0) { end = NR; open = 0 } }
		END { if (open) end = -1; print end }' "$1"
}

N=1
step() {
	say "  $N. $*"
	N=$((N + 1))
}
more() { say "     $*"; }

# ---------------------------------------------------------------- the build tools

@@FRAGMENTS@@

# ---------------------------------------------------------------- the CI files

@@CI_FILES@@

# ---------------------------------------------------------------- the build tool

TOOL=
for t in $TOOLS; do
	if "detect_$t"; then
		TOOL=$t
		break
	fi
done
[ -n "$TOOL" ] || die "this folder has no @@PROJECTS@@. Run the command in the folder of your project."

# ---------------------------------------------------------------- the questions

if [ -z "$REPO" ]; then
	[ -n "$TTY" ] || die "name the repository: curl -fsSL $URL/setup.sh | sh -s -- REPO"
	ask "Which repository do you publish to? "
	REPO=$ANSWER
fi
case $REPO in
'' | *[!a-z0-9._-]* | [._-]*) die "\"$REPO\" is not a repository name. Use the name the server shows, such as releases." ;;
esac

if [ -z "$CI_SET" ] && [ -n "$TTY" ]; then
	printf '%s\n' "Where will you publish from?" \
		"  1  Your computer only" \
		"  2  GitHub Actions" \
		"  3  Jenkins with a Jenkinsfile" \
		"  4  Jenkins freestyle job" >/dev/tty
	ask "Type 1, 2, 3 or 4 [1]: "
	case $ANSWER in
	'' | 1) CI= ;;
	2) CI=github ;;
	3) CI=jenkins-pipeline ;;
	4) CI=jenkins-freestyle ;;
	*) die "\"$ANSWER\" is not 1, 2, 3 or 4" ;;
	esac
fi
case $CI in
none) CI= ;;
'' | github | jenkins-pipeline | jenkins-freestyle) ;;
*) die "--ci takes none, github, jenkins-pipeline or jenkins-freestyle, not \"$CI\"" ;;
esac

# ---------------------------------------------------------------- CI

# ci_text KIND prints the CI file KIND (github, jenkins, freestyle) of the build tool for REPO.
ci_text() { "ci_${1}_$TOOL" | sed "s|%REPO%|$REPO|g" | "ci_edit_$TOOL"; }

# ci_file FILE CONTENT writes a new CI file. It keeps a file with other content.
ci_file() {
	stage_file "$1"
	_w=$(stage_path "$1")
	if [ ! -f "$1" ]; then
		printf '%s\n' "$2" >"$_w"
		plan "$1" "create it" "$2"
	elif [ "$(tr -d '\r' <"$1")" != "$2" ]; then
		manual "$1" "the file exists with other content. Compare it with this one" "$2"
	fi
}

setup_ci() {
	case $CI in
	github) ci_file .github/workflows/publish.yml "$(ci_text github)" ;;
	jenkins-pipeline) ci_file Jenkinsfile "$(ci_text jenkins)" ;;
	esac
}

# ---------------------------------------------------------------- run

"apply_$TOOL"
[ -z "$CI" ] || setup_ci

# Compare each working copy with its file: a new file, an edit, or no change.
: >"$TMP/new"
: >"$TMP/edit"
while IFS= read -r f; do
	w=$(stage_path "$f")
	if [ -f "$f" ]; then
		cmp -s "$f" "$w" || printf '%s\n' "$f" >>"$TMP/edit"
	elif [ -s "$w" ]; then
		printf '%s\n' "$f" >>"$TMP/new"
	fi
done <"$TMP/files"

if [ -s "$TMP/new" ] || [ -s "$TMP/edit" ]; then
	say "Changes to the project to publish to $REPO on $URL:"
	say ""
	cat "$TMP/plan"
	say ""
	if [ -s "$TMP/edit" ] && [ -z "$YES" ]; then
		[ -n "$TTY" ] || die "run the command again with --yes to change these files"
		ask "Change these files? [y/N] "
		case $ANSWER in
		y | Y | yes | Yes | YES) ;;
		*) die "no file changed" ;;
		esac
	fi
	while IFS= read -r f; do
		case $f in */*) mkdir -p "${f%/*}" ;; esac
		cat "$(stage_path "$f")" >"$f"
	done <<EOF
$(cat "$TMP/new" "$TMP/edit")
EOF
fi

if [ -s "$TMP/manual" ]; then
	say "Paste these blocks by hand. The file is too unusual to change safely."
	cat "$TMP/manual"
	say ""
fi

if [ -s "$TMP/notes" ]; then
	cat "$TMP/notes"
	say ""
fi

if [ -s "$TMP/new" ] || [ -s "$TMP/edit" ]; then
	if [ -s "$TMP/manual" ]; then say "Almost done: paste the blocks above, then commit. Changed files:"; else say "Done. Changed files:"; fi
	cat "$TMP/new" "$TMP/edit" | sed 's/^/  /'
	say ""
	say "Next:"
	step "Commit these files."
else
	say "Done. The project is already set up. No file changed."
	say ""
	say "Next:"
	step "Commit the files if you have not yet."
fi
"next_$TOOL"
step "Run $("publish_cmd_$TOOL") and click Approve in your browser the first time."
[ -z "$CI" ] || step "Ask an admin to add a CI pipeline on Settings › CI pipelines."
case $CI in
github)
	more "The CI pipeline must allow tags v*. The workflow publishes when you push a tag such as v1.0.0."
	;;
jenkins-pipeline)
	step "$JENKINS_NOTE"
	;;
jenkins-freestyle)
	step "$JENKINS_NOTE"
	step "Set up the freestyle job in Jenkins:"
	more "a. Open the job, click Configure."
	more "b. Under Environment, tick \"Use secret text(s) or file(s)\"."
	more "c. Under Bindings, click Add, then \"Secret text\"."
	more "d. Set Variable OIDC_TOKEN and Credentials $CREDENTIAL."
	more "e. Click \"Add build step\", then \"Execute shell\" (\"Execute Windows batch command\" on Windows agents)."
	more "f. Type: $(ci_text freestyle)"
	more "g. Click Save, then Build Now."
	;;
esac
[ ! -s "$TMP/manual" ] || exit 1
