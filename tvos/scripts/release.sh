#!/bin/bash
# Archives, signs and (with UPLOAD=1) uploads the tvOS app to App Store
# Connect / TestFlight. Run on a Mac with Xcode and xcodegen:
#
#   ASC_KEY_ID=X34NK89X8G ASC_ISSUER_ID=f26d2ee4-... \
#   ASC_KEY_PATH=~/.private_keys/AuthKey_X34NK89X8G.p8 \
#   DIST_P12=~/.private_keys/okaeri-distribution.p12 \
#   UPLOAD=1 tvos/scripts/release.sh
#
# PYTHON picks an interpreter with PyJWT (default python3).
# DIST_P12 is the team's Apple Distribution certificate (empty password).
# It is imported into a throwaway keychain that is deleted afterwards.
set -euo pipefail

cd "$(dirname "$0")/.."
: "${ASC_KEY_ID:?}" "${ASC_ISSUER_ID:?}" "${ASC_KEY_PATH:?}" "${DIST_P12:?}"
export ASC_KEY_ID ASC_ISSUER_ID ASC_KEY_PATH
PROFILE_NAME="${PROFILE_NAME:-urdp tvOS App Store}"
TEAM=66FGZV449C
BUNDLE_ID=com.okaericommunications.urdp
BUILD_NUMBER="${BUILD_NUMBER:-$(date -u +%Y%m%d%H%M)}"
OUT="${OUT:-build/release}"

work=$(mktemp -d)
keychain="$work/signing.keychain-db"
keychain_password=$(openssl rand -hex 24)
saved_keychains=$(security list-keychains -d user | xargs)
cleanup() {
    # shellcheck disable=SC2086
    security list-keychains -d user -s $saved_keychains >/dev/null 2>&1 || true
    security delete-keychain "$keychain" >/dev/null 2>&1 || true
    rm -rf "$work"
}
trap cleanup EXIT

# Signing identity: split the PKCS#12 (OpenSSL 3 writes ones `security`
# can't read) and import the key and certificate separately.
umask 077
openssl pkcs12 -in "$DIST_P12" -passin pass: -nocerts -nodes | openssl pkey -out "$work/key.pem"
openssl pkcs12 -in "$DIST_P12" -passin pass: -clcerts -nokeys -out "$work/cert.pem"
security create-keychain -p "$keychain_password" "$keychain"
security set-keychain-settings -lut 3600 "$keychain"
security unlock-keychain -p "$keychain_password" "$keychain"
security import "$work/key.pem" -k "$keychain" -T /usr/bin/codesign -T /usr/bin/security >/dev/null
security import "$work/cert.pem" -k "$keychain" >/dev/null
curl -fsS https://www.apple.com/certificateauthority/AppleWWDRCAG3.cer -o "$work/wwdr.cer"
security import "$work/wwdr.cer" -k "$keychain" >/dev/null || true
security set-key-partition-list -S apple-tool:,apple: -s -k "$keychain_password" "$keychain" >/dev/null
# shellcheck disable=SC2086
security list-keychains -d user -s "$keychain" $saved_keychains
identity=$(security find-identity -v -p codesigning "$keychain" | grep -o '"Apple Distribution: [^"]*"' | head -1 | tr -d '"')
[[ -n "$identity" ]] || { echo "no Apple Distribution identity in $DIST_P12" >&2; exit 1; }

# Provisioning profile, fetched from App Store Connect.
profiles="$HOME/Library/MobileDevice/Provisioning Profiles"
mkdir -p "$profiles"
uuid=$("${PYTHON:-python3}" scripts/asc.py profile "$PROFILE_NAME" "$work/profile.mobileprovision")
cp "$work/profile.mobileprovision" "$profiles/$uuid.mobileprovision"

xcodegen generate -q
rm -rf "$OUT"
mkdir -p "$OUT"
xcodebuild -project urdp.xcodeproj -scheme urdp -configuration Release \
    -destination "generic/platform=tvOS" \
    -archivePath "$OUT/urdp.xcarchive" -derivedDataPath build/DerivedData-release \
    CODE_SIGN_STYLE=Manual DEVELOPMENT_TEAM="$TEAM" CODE_SIGN_IDENTITY="$identity" \
    PROVISIONING_PROFILE_SPECIFIER="$PROFILE_NAME" OTHER_CODE_SIGN_FLAGS="--keychain $keychain" \
    CURRENT_PROJECT_VERSION="$BUILD_NUMBER" \
    archive >"$OUT/archive.log" 2>&1 || {
    grep -E "error:|BUILD FAILED|ARCHIVE FAILED" "$OUT/archive.log" | sort -u >&2
    echo "archive failed; see $OUT/archive.log" >&2
    exit 1
}

destination=export
[[ -n "${UPLOAD:-}" ]] && destination=upload
cat > "$work/export.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>method</key><string>app-store-connect</string>
	<key>destination</key><string>$destination</string>
	<key>teamID</key><string>$TEAM</string>
	<key>signingStyle</key><string>manual</string>
	<key>signingCertificate</key><string>Apple Distribution</string>
	<key>provisioningProfiles</key>
	<dict><key>$BUNDLE_ID</key><string>$PROFILE_NAME</string></dict>
	<key>manageAppVersionAndBuildNumber</key><false/>
</dict>
</plist>
EOF
xcodebuild -exportArchive -archivePath "$OUT/urdp.xcarchive" -exportPath "$OUT" \
    -exportOptionsPlist "$work/export.plist" \
    -allowProvisioningUpdates -authenticationKeyPath "$ASC_KEY_PATH" \
    -authenticationKeyID "$ASC_KEY_ID" -authenticationKeyIssuerID "$ASC_ISSUER_ID"
echo "build $BUILD_NUMBER: ${destination}ed ($OUT)"
