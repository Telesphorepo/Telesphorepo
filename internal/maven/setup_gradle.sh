# Gradle: the Telesphorepo plugin in the settings file.
VERSION='@@VERSION@@'
GRADLE_PLUGIN_ID='com.telesphorepo.publish'

detect_gradle() {
	if [ -f settings.gradle.kts ]; then
		GRADLE_SETTINGS=settings.gradle.kts GRADLE_KTS=1
	elif [ -f settings.gradle ]; then
		GRADLE_SETTINGS=settings.gradle GRADLE_KTS=
	else
		return 1
	fi
}

gradle_build() {
	if [ -f gradlew ]; then printf './gradlew'; else printf 'gradle'; fi
}

publish_cmd_gradle() { printf '%s publish' "$(gradle_build)"; }

# ci_edit_gradle runs a CI file through: a project without the wrapper runs gradle.
ci_edit_gradle() {
	if [ -f gradlew ]; then cat; else sed 's|\./gradlew|gradle|g'; fi
}

apply_gradle() {
	f=$GRADLE_SETTINGS
	stage_file "$f"
	w=$(stage_path "$f")
	if [ -n "$GRADLE_KTS" ]; then
		idline="id(\"$GRADLE_PLUGIN_ID\") version \"$VERSION\""
		block="telesphorepo {${NL}    url = \"$URL\"${NL}    repository = \"$REPO\""
		oidc="    oidcTokenEnv = \"OIDC_TOKEN\""
	else
		idline="id '$GRADLE_PLUGIN_ID' version '$VERSION'"
		block="telesphorepo {${NL}    url = '$URL'${NL}    repository = '$REPO'"
		oidc="    oidcTokenEnv = 'OIDC_TOKEN'"
	fi
	case $CI in jenkins-*) block="$block${NL}$oidc" ;; esac
	block="$block${NL}}"
	plugins="plugins {${NL}    $idline${NL}}"

	if ! grep -q "$GRADLE_PLUGIN_ID" "$w"; then
		start=$(block_start "$w" plugins)
		end=$(block_end "$w" plugins)
		pm=$(block_end "$w" pluginManagement)
		bs=$(block_end "$w" buildscript)
		if [ "$start" -gt 0 ] && [ "$end" -gt "$start" ]; then
			stage_insert_after "$f" "$start" "    $idline"
			plan "$f" "add the plugin to the plugins block" "    $idline"
		elif [ "$start" -gt 0 ] || [ "$pm" -lt 0 ] || [ "$bs" -lt 0 ] || grep -Eq '^plugins[[:space:]]*$' "$w"; then
			manual "$f" "add the id line to the plugins block, or this block at the top (only pluginManagement can come before it)" "$plugins"
		else
			after=$pm
			[ "$bs" -gt "$after" ] && after=$bs
			if [ "$after" -gt 0 ]; then
				stage_insert_after "$f" "$after" "${NL}$plugins"
			else
				# At the top, after the comments and imports that open the file.
				after=$(awk '/^[ \t\r]*$/ || /^[ \t]*\/\// || /^import[ \t]/ || /^@file:/ { n = NR; next } { exit } END { print n + 0 }' "$w")
				if [ "$after" -gt 0 ]; then
					stage_insert_after "$f" "$after" "${NL}$plugins${NL}"
				else
					stage_insert_after "$f" 0 "$plugins${NL}"
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
				stage_insert_after "$f" "$(block_start "$w" telesphorepo)" "$oidc"
				plan "$f" "add the Jenkins identity token to the telesphorepo block" "$oidc"
			fi
			;;
		esac
	else
		stage_append "$f" "${NL}$block"
		plan "$f" "add the telesphorepo block at the end" "$block"
	fi
}

# gradle_build_file prints the root build file of the Gradle project.
gradle_build_file() {
	if [ -n "$GRADLE_KTS" ]; then b=build.gradle.kts o=build.gradle; else b=build.gradle o=build.gradle.kts; fi
	if [ ! -f "$b" ] && [ -f "$o" ]; then b=$o; fi
	printf '%s' "$b"
}

# gradle_has NAME tells whether the root build file or gradle.properties sets NAME.
gradle_has() {
	for _f in "$(gradle_build_file)" gradle.properties; do
		[ -f "$_f" ] && grep -Eq "^[[:space:]]*$1[[:space:]]*=" "$_f" && return 0
	done
	return 1
}

next_gradle() {
	if ! gradle_has group || ! gradle_has version; then
		step "Set group and version in $(gradle_build_file), for example group = \"com.example\" and version = \"1.0.0\"."
	fi
}
