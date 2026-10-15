#!/bin/sh
# Telesphorepo project setup. Run it in the folder of a Gradle or Maven project:
#
#   curl -fsSL @@URL@@/setup.sh | sh -s -- REPO [--ci none|github|jenkins-pipeline|jenkins-freestyle] [--yes]
#
# It sets up the project to publish to the repository REPO on @@URL@@. It changes only files of
# the project, never ~/.m2 or ~/.gradle, and it writes no secret. A second run changes nothing.
set -eu

URL='@@URL@@'
VERSION='@@VERSION@@'
CREDENTIAL='@@CREDENTIAL@@'
JENKINS_NOTE='@@JENKINS_NOTE@@'
PLUGIN_ID='com.telesphorepo.publish'
EXTENSION_ID='telesphorepo-maven-extension'
NL='
'
CR=$(printf '\r')

github_workflow() {
	cat <<'TELESPHOREPO_EOF'
@@GITHUB_WORKFLOW@@
TELESPHOREPO_EOF
}

jenkinsfile() {
	cat <<'TELESPHOREPO_EOF'
@@JENKINSFILE@@
TELESPHOREPO_EOF
}

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

Run it in the folder with settings.gradle.kts, settings.gradle or pom.xml.
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

# ---------------------------------------------------------------- the build tool

if [ -f settings.gradle.kts ]; then
	TOOL=gradle SETTINGS=settings.gradle.kts KTS=1
elif [ -f settings.gradle ]; then
	TOOL=gradle SETTINGS=settings.gradle KTS=
elif [ -f pom.xml ]; then
	TOOL=maven
else
	die "this folder has no settings.gradle.kts, settings.gradle or pom.xml. Run the command in the folder of your project."
fi

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

# wpath FILE prints the path of the working copy of FILE.
wpath() { printf '%s/w/%s' "$TMP" "$(printf '%s' "$1" | tr '/' '_')"; }

# work FILE starts the working copy of FILE: a copy, or an empty file when FILE does not exist.
work() {
	_w=$(wpath "$1")
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

# insert_after FILE N TEXT puts the lines of TEXT after line N of the working copy of FILE (0:
# before the first line). At N = -1 it adds them at the end.
insert_after() {
	_w=$(wpath "$1")
	awk -v n="$2" -v text="$3" -v eol="$(eol "$_w")" '
		function emit(   i, k, parts) { k = split(text, parts, "\n"); for (i = 1; i <= k; i++) printf "%s%s\n", parts[i], eol }
		n == 0 && NR == 1 { emit(); done = 1 }
		{ print }
		NR == n { emit(); done = 1 }
		END { if (!done) emit() }' "$_w" >"$_w.new"
	mv "$_w.new" "$_w"
}

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

# ---------------------------------------------------------------- Gradle

gradle_build() {
	if [ -f gradlew ]; then printf './gradlew'; else printf 'gradle'; fi
}

setup_gradle() {
	f=$SETTINGS
	work "$f"
	w=$(wpath "$f")
	if [ -n "$KTS" ]; then
		idline="id(\"$PLUGIN_ID\") version \"$VERSION\""
		block="telesphorepo {${NL}    url = \"$URL\"${NL}    repository = \"$REPO\""
		oidc="    oidcTokenEnv = \"OIDC_TOKEN\""
	else
		idline="id '$PLUGIN_ID' version '$VERSION'"
		block="telesphorepo {${NL}    url = '$URL'${NL}    repository = '$REPO'"
		oidc="    oidcTokenEnv = 'OIDC_TOKEN'"
	fi
	case $CI in jenkins-*) block="$block${NL}$oidc" ;; esac
	block="$block${NL}}"
	plugins="plugins {${NL}    $idline${NL}}"

	if ! grep -q "$PLUGIN_ID" "$w"; then
		start=$(block_start "$w" plugins)
		end=$(block_end "$w" plugins)
		pm=$(block_end "$w" pluginManagement)
		bs=$(block_end "$w" buildscript)
		if [ "$start" -gt 0 ] && [ "$end" -gt "$start" ]; then
			insert_after "$f" "$start" "    $idline"
			plan "$f" "add the plugin to the plugins block" "    $idline"
		elif [ "$start" -gt 0 ] || [ "$pm" -lt 0 ] || [ "$bs" -lt 0 ] || grep -Eq '^plugins[[:space:]]*$' "$w"; then
			manual "$f" "add the id line to the plugins block, or this block at the top (only pluginManagement can come before it)" "$plugins"
		else
			after=$pm
			[ "$bs" -gt "$after" ] && after=$bs
			if [ "$after" -gt 0 ]; then
				insert_after "$f" "$after" "${NL}$plugins"
			else
				# At the top, after the comments and imports that open the file.
				after=$(awk '/^[ \t\r]*$/ || /^[ \t]*\/\// || /^import[ \t]/ || /^@file:/ { n = NR; next } { exit } END { print n + 0 }' "$w")
				if [ "$after" -gt 0 ]; then
					insert_after "$f" "$after" "${NL}$plugins${NL}"
				else
					insert_after "$f" 0 "$plugins${NL}"
				fi
			fi
			plan "$f" "add the plugins block at the top" "$plugins"
		fi
	fi

	if grep -Eq '^telesphorepo[[:space:]]*[{]' "$w"; then
		if ! grep -q "repository *= *[\"']$REPO[\"']" "$w"; then
			note "$f has a telesphorepo block for another repository. Check its url and repository."
		fi
		case $CI in
		jenkins-*)
			if ! grep -q 'oidcTokenEnv' "$w"; then
				insert_after "$f" "$(block_start "$w" telesphorepo)" "$oidc"
				plan "$f" "add the Jenkins identity token to the telesphorepo block" "$oidc"
			fi
			;;
		esac
	else
		insert_after "$f" -1 "${NL}$block"
		plan "$f" "add the telesphorepo block at the end" "$block"
	fi
}

# ---------------------------------------------------------------- Maven

setup_maven() {
	f=.mvn/extensions.xml
	ext="  <extension>${NL}    <groupId>com.telesphorepo</groupId>${NL}    <artifactId>$EXTENSION_ID</artifactId>${NL}    <version>$VERSION</version>${NL}  </extension>"
	work "$f"
	w=$(wpath "$f")
	if [ ! -f "$f" ]; then
		printf '<extensions>\n%s\n</extensions>\n' "$ext" >"$w"
		plan "$f" "create it" "$(cat "$w")"
	elif ! grep -q "$EXTENSION_ID" "$w"; then
		lines=$(grep -En '^[[:space:]]*</extensions>[[:space:]]*$' "$w" | cut -d: -f1)
		if [ "$(grep -c '</extensions>' "$w")" = 1 ] && [ -n "$lines" ]; then
			insert_after "$f" "$((lines - 1))" "$ext"
			plan "$f" "add the Telesphorepo extension" "$ext"
		else
			manual "$f" "add this extension inside the extensions element" "$ext"
		fi
	fi

	f=.mvn/maven.config
	line="-Dtelesphorepo.url=$URL"
	work "$f"
	w=$(wpath "$f")
	if [ ! -f "$f" ]; then
		printf '%s\n' "$line" >"$w"
		plan "$f" "create it" "$line"
	elif grep -q -e '-Dtelesphorepo\.url=' "$w"; then
		grep -qF -e "$line" "$w" || note "$f names another Telesphorepo server. Check the line -Dtelesphorepo.url."
	else
		insert_after "$f" -1 "$line"
		plan "$f" "add the server address" "$line"
	fi

	f=pom.xml
	work "$f"
	w=$(wpath "$f")
	if grep -q '<distributionManagement' "$w"; then
		grep -qF "$URL/maven/$REPO<" "$w" || note "pom.xml has a distributionManagement block that does not name $URL/maven/$REPO. Check it."
	else
		i=$(awk '/^[ \t]*<modelVersion>/ { match($0, /^[ \t]*/); print substr($0, 1, RLENGTH); found = 1; exit } END { if (!found) print "" }' "$w")
		[ -n "$i" ] || i='  '
		dm="$i<distributionManagement>${NL}$i$i<repository>${NL}$i$i$i<id>$REPO</id>${NL}$i$i$i<url>$URL/maven/$REPO</url>${NL}$i$i</repository>"
		dm="$dm${NL}$i$i<snapshotRepository>${NL}$i$i$i<id>$REPO</id>${NL}$i$i$i<url>$URL/maven/$REPO</url>${NL}$i$i</snapshotRepository>${NL}$i</distributionManagement>"
		lines=$(grep -En '^[[:space:]]*</project>[[:space:]]*$' "$w" | cut -d: -f1)
		if [ "$(grep -c '</project>' "$w")" = 1 ] && [ -n "$lines" ]; then
			insert_after "$f" "$((lines - 1))" "$dm"
			plan "$f" "add distributionManagement" "$dm"
		else
			manual "$f" "add this block inside the project element" "$dm"
		fi
	fi
}

# ---------------------------------------------------------------- CI

# ci_file FILE CONTENT writes a new CI file. It keeps a file with other content.
ci_file() {
	work "$1"
	_w=$(wpath "$1")
	if [ ! -f "$1" ]; then
		printf '%s\n' "$2" >"$_w"
		plan "$1" "create it" "$2"
	elif [ "$(tr -d '\r' <"$1")" != "$2" ]; then
		manual "$1" "the file exists with other content. Compare it with this one" "$2"
	fi
}

setup_ci() {
	if [ "$TOOL" = gradle ]; then
		g=$(gradle_build)
		build="$g build" publish="$g publish" ghpublish="$g publish"
	else
		build="mvn -B verify" publish="mvn -B deploy -DskipTests -Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN" ghpublish="mvn -B deploy"
	fi
	case $CI in
	github)
		ci_file .github/workflows/publish.yml "$(github_workflow | sed "s|%PUBLISH%|$ghpublish|")"
		;;
	jenkins-pipeline)
		ci_file Jenkinsfile "$(jenkinsfile | sed -e "s|%REPO%|$REPO|" -e "s|%BUILD%|$build|" -e "s|%PUBLISH%|$publish|")"
		;;
	esac
}

# ---------------------------------------------------------------- run

if [ "$TOOL" = gradle ]; then setup_gradle; else setup_maven; fi
[ -z "$CI" ] || setup_ci

# Compare each working copy with its file: a new file, an edit, or no change.
: >"$TMP/new"
: >"$TMP/edit"
while IFS= read -r f; do
	w=$(wpath "$f")
	if [ -f "$f" ]; then
		cmp -s "$f" "$w" || printf '%s\n' "$f" >>"$TMP/edit"
	elif [ -s "$w" ]; then
		printf '%s\n' "$f" >>"$TMP/new"
	fi
done <"$TMP/files"

if [ -s "$TMP/new" ] || [ -s "$TMP/edit" ]; then
	say "Changes to the project for $URL/maven/$REPO:"
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
		cat "$(wpath "$f")" >"$f"
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

# build_file prints the root build file of the Gradle project.
build_file() {
	if [ -n "$KTS" ]; then b=build.gradle.kts o=build.gradle; else b=build.gradle o=build.gradle.kts; fi
	if [ ! -f "$b" ] && [ -f "$o" ]; then b=$o; fi
	printf '%s' "$b"
}

# has_coordinate NAME tells whether the root build file or gradle.properties sets NAME.
has_coordinate() {
	for _f in "$(build_file)" gradle.properties; do
		[ -f "$_f" ] && grep -Eq "^[[:space:]]*$1[[:space:]]*=" "$_f" && return 0
	done
	return 1
}

N=1
step() {
	say "  $N. $*"
	N=$((N + 1))
}
more() { say "     $*"; }

if [ "$TOOL" = gradle ]; then run="$(gradle_build) publish"; else run="mvn deploy"; fi
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
if [ "$TOOL" = gradle ] && { ! has_coordinate group || ! has_coordinate version; }; then
	step "Set group and version in $(build_file), for example group = \"com.example\" and version = \"1.0.0\"."
fi
step "Run $run and click Approve in your browser the first time."
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
	if [ "$TOOL" = gradle ]; then shell="$(gradle_build) publish"; else shell="mvn -B deploy -Dtelesphorepo.oidcTokenEnv=OIDC_TOKEN"; fi
	step "Set up the freestyle job in Jenkins:"
	more "a. Open the job, click Configure."
	more "b. Under Environment, tick \"Use secret text(s) or file(s)\"."
	more "c. Under Bindings, click Add, then \"Secret text\"."
	more "d. Set Variable OIDC_TOKEN and Credentials $CREDENTIAL."
	more "e. Click \"Add build step\", then \"Execute shell\" (\"Execute Windows batch command\" on Windows agents)."
	more "f. Type: $shell"
	more "g. Click Save, then Build Now."
	;;
esac
[ ! -s "$TMP/manual" ] || exit 1