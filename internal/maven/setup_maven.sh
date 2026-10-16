# Maven: the Telesphorepo extension, the server address and distributionManagement.
VERSION='@@VERSION@@'
MAVEN_EXTENSION_ID='telesphorepo-maven-extension'

detect_maven() { [ -f pom.xml ]; }

publish_cmd_maven() { printf 'mvn deploy'; }

ci_edit_maven() { cat; }

next_maven() { :; }

apply_maven() {
	f=.mvn/extensions.xml
	ext="  <extension>${NL}    <groupId>com.telesphorepo</groupId>${NL}    <artifactId>$MAVEN_EXTENSION_ID</artifactId>${NL}    <version>$VERSION</version>${NL}  </extension>"
	stage_file "$f"
	w=$(stage_path "$f")
	if [ ! -f "$f" ]; then
		printf '<extensions>\n%s\n</extensions>\n' "$ext" >"$w"
		plan "$f" "create it" "$(cat "$w")"
	elif ! grep -q "$MAVEN_EXTENSION_ID" "$w"; then
		lines=$(grep -En '^[[:space:]]*</extensions>[[:space:]]*$' "$w" | cut -d: -f1)
		if [ "$(grep -c '</extensions>' "$w")" = 1 ] && [ -n "$lines" ]; then
			stage_insert_after "$f" "$((lines - 1))" "$ext"
			plan "$f" "add the Telesphorepo extension" "$ext"
		else
			manual "$f" "add this extension inside the extensions element" "$ext"
		fi
	fi

	f=.mvn/maven.config
	line="-Dtelesphorepo.url=$URL"
	stage_file "$f"
	w=$(stage_path "$f")
	if [ ! -f "$f" ]; then
		printf '%s\n' "$line" >"$w"
		plan "$f" "create it" "$line"
	elif grep -q -e '-Dtelesphorepo\.url=' "$w"; then
		grep -qF -e "$line" "$w" || note "$f names another Telesphorepo server. Check the line -Dtelesphorepo.url."
	else
		stage_append "$f" "$line"
		plan "$f" "add the server address" "$line"
	fi

	f=pom.xml
	stage_file "$f"
	w=$(stage_path "$f")
	if grep -q '<distributionManagement' "$w"; then
		grep -qF "$URL/maven/$REPO<" "$w" || note "pom.xml has a distributionManagement block that does not name $URL/maven/$REPO. Check it."
	else
		i=$(awk '/^[ \t]*<modelVersion>/ { match($0, /^[ \t]*/); print substr($0, 1, RLENGTH); found = 1; exit } END { if (!found) print "" }' "$w")
		[ -n "$i" ] || i='  '
		dm="$i<distributionManagement>${NL}$i$i<repository>${NL}$i$i$i<id>$REPO</id>${NL}$i$i$i<url>$URL/maven/$REPO</url>${NL}$i$i</repository>"
		dm="$dm${NL}$i$i<snapshotRepository>${NL}$i$i$i<id>$REPO</id>${NL}$i$i$i<url>$URL/maven/$REPO</url>${NL}$i$i</snapshotRepository>${NL}$i</distributionManagement>"
		lines=$(grep -En '^[[:space:]]*</project>[[:space:]]*$' "$w" | cut -d: -f1)
		if [ "$(grep -c '</project>' "$w")" = 1 ] && [ -n "$lines" ]; then
			stage_insert_after "$f" "$((lines - 1))" "$dm"
			plan "$f" "add distributionManagement" "$dm"
		else
			manual "$f" "add this block inside the project element" "$dm"
		fi
	fi
}
