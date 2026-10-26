# Use raw files for an updater

An application that updates itself needs two kinds of files: the builds, which never change once
published, and one small file that names the newest build. A raw repository holds both. This
guide sets one up so that a retry never breaks a build, two CI jobs never overwrite each other's
`latest.json`, and old clients keep working after the format of `latest.json` changes.

## Lay out the paths

```
builds/41/app.jar
builds/42/app.jar
v1/latest.json        {"build": 42, "url": "builds/42/app.jar"}
```

Put a version in the path of `latest.json` from the first release. When its format has to change
in a way old clients cannot read, start writing `v2/latest.json` and leave `v1/latest.json` where
it is: the clients you shipped keep reading `v1/`, and new ones read `v2/`. If you shipped without
a version in the path, do the same with `v2/` as the first versioned folder and leave the old file
alone.

## Set the rules of the repository

Create a raw repository and open **Settings › Repositories › your repository**. Set **Replace
files** to `deny` and add one rule under **Rules by path**: `v1/latest.json` with `allow`. The
config, as `PATCH /api/repos/{name}` takes it:

```json
{
  "overwrite": "deny",
  "rules": [
    {"match": "v1/latest.json", "overwrite": "allow"}
  ]
}
```

The first rule whose pattern matches a path decides; any other path follows **Replace files**.
`*` matches one name and `**` any depth, so `builds/**` is every file under `builds/`. With
`deny`, an upload of other bytes to a stored build fails with 409, and an upload of the same
bytes succeeds, so a CI job that retries after a timeout does no harm. A person who may delete
files can still delete a build; a CI session cannot.

With edge delivery on, every path under `deny` is served from the region nearest the client, and
`v1/latest.json` from the home bucket, so a client sees a new `latest.json` as soon as it is
published.

## Publish a build

Run the CLI in the project that `telesphorepo setup` set up for the repository (its
`telesphorepo.toml` names the server and the repository). Upload the build into its own folder
first, then point `latest.json` at it:

```sh
telesphorepo publish --into "builds/$BUILD" app.jar
```

## Update latest.json without losing a race

Two jobs that finish close together both read `latest.json`, and the slower one could put an
older build back. Publish it with `--if-match` and the ETag you read: the server stores the file
only if it is still the one you read, and otherwise the CLI exits with code 3. Read the file
again and decide again. A build older than the one already named stops there.

```sh
#!/bin/sh
# Run with the number of the build that was just published.
set -u
build=$1
url="https://repo.example.com/raw/updater/v1/latest.json"

while :; do
  status=$(curl -sS -D headers.txt -o current.json -w '%{http_code}' "$url") || exit 1
  if [ "$status" = 404 ]; then
    when=--if-new
  elif [ "$status" = 200 ]; then
    current=$(jq -r .build current.json)
    if [ "$current" -ge "$build" ]; then
      echo "latest.json names build $current; leaving it"
      exit 0
    fi
    etag=$(grep -i '^etag:' headers.txt | cut -d' ' -f2 | tr -d '\r')
    when="--if-match $etag"
  else
    echo "GET latest.json answered $status" >&2
    exit 1
  fi

  jq -n --argjson b "$build" '{build: $b, url: "builds/\($b)/app.jar"}' > latest.json
  # shellcheck disable=SC2086
  telesphorepo publish --into v1 $when latest.json
  case $? in
    0) exit 0 ;;
    3) echo "latest.json changed while this job ran; reading it again" ;;
    *) exit 1 ;;
  esac
done
```

`--if-new` covers the first release, when there is no `latest.json` yet. A private repository
needs a read token for the `curl` lines: add `-u "ci:$READ_TOKEN"`.

## Check for updates from the client

Read `v1/latest.json` once at a random moment within the first minutes after start, then on a
fixed interval, such as every six hours. The random offset keeps a fleet that starts at the same
time (after a holiday, a power cut, a release) from asking in the same second. A 404 means no
release yet. Download the build from the `url` that `latest.json` names: builds never change, so
a client can keep a download it already has.
