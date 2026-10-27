package rpm

// The tags this module reads and writes (rpm's include/rpm/rpmtag.h).

// Signature header tags.
const (
	sigTagDSA             = 267 // header-only OpenPGP signature: DSA, ECDSA, EdDSA
	sigTagRSA             = 268 // header-only OpenPGP signature: RSA
	sigTagSHA1            = 269
	sigTagLongSize        = 270
	sigTagLongArchiveSize = 271
	sigTagSHA256          = 273 // hex SHA-256 of the header
	sigTagOpenPGP         = 278 // v6 packages: base64 OpenPGP signatures
	sigTagSHA3_256        = 279
	sigTagReserved        = 999 // v6 reserved space
	sigTagSize            = 1000
	sigTagPGP             = 1002 // header+payload signature: RSA
	sigTagMD5             = 1004
	sigTagGPG             = 1005 // header+payload signature: DSA and others
	sigTagPGP5            = 1006
	sigTagPayloadSize     = 1007
	sigTagReservedSpace   = 1008
)

// signatureTags are the signature tags a new signature replaces.
var signatureTags = []uint32{sigTagDSA, sigTagRSA, sigTagOpenPGP, sigTagPGP, sigTagGPG, sigTagPGP5, sigTagReserved, sigTagReservedSpace}

// Header tags.
const (
	tagHeaderI18NTable   = 100
	tagName              = 1000
	tagVersion           = 1001
	tagRelease           = 1002
	tagEpoch             = 1003
	tagSummary           = 1004
	tagDescription       = 1005
	tagBuildTime         = 1006
	tagBuildHost         = 1007
	tagSize              = 1009
	tagVendor            = 1011
	tagLicense           = 1014
	tagPackager          = 1015
	tagGroup             = 1016
	tagURL               = 1020
	tagOS                = 1021
	tagArch              = 1022
	tagFileSizes         = 1028
	tagFileModes         = 1030
	tagFileRdevs         = 1033
	tagFileMtimes        = 1034
	tagFileDigests       = 1035
	tagFileLinkTos       = 1036
	tagFileFlags         = 1037
	tagFileUserName      = 1039
	tagFileGroupName     = 1040
	tagSourceRPM         = 1044
	tagFileVerifyFlags   = 1045
	tagArchiveSize       = 1046
	tagProvideName       = 1047
	tagRequireFlags      = 1048
	tagRequireName       = 1049
	tagRequireVersion    = 1050
	tagConflictFlags     = 1053
	tagConflictName      = 1054
	tagConflictVersion   = 1055
	tagChangelogTime     = 1080
	tagChangelogName     = 1081
	tagChangelogText     = 1082
	tagObsoleteName      = 1090
	tagFileDevices       = 1095
	tagFileInodes        = 1096
	tagFileLangs         = 1097
	tagSourcePackage     = 1106
	tagProvideFlags      = 1112
	tagProvideVersion    = 1113
	tagObsoleteFlags     = 1114
	tagObsoleteVersion   = 1115
	tagDirIndexes        = 1116
	tagBaseNames         = 1117
	tagDirNames          = 1118
	tagPayloadFormat     = 1124
	tagPayloadCompressor = 1125
	tagPayloadFlags      = 1126
	tagLongSize          = 5009
	tagFileDigestAlgo    = 5011
	tagRecommendName     = 5046
	tagRecommendVersion  = 5047
	tagRecommendFlags    = 5048
	tagSuggestName       = 5049
	tagSuggestVersion    = 5050
	tagSuggestFlags      = 5051
	tagSupplementName    = 5052
	tagSupplementVersion = 5053
	tagSupplementFlags   = 5054
	tagEnhanceName       = 5055
	tagEnhanceVersion    = 5056
	tagEnhanceFlags      = 5057
	tagPayloadDigest     = 5092
	tagPayloadDigestAlgo = 5093
	tagPayloadSizeAlt    = 5113
	tagRPMFormat         = 5114
)

// The OpenPGP hash algorithms of a payload digest.
const (
	hashSHA256 = 8
	hashSHA512 = 10
)

// Dependency flags (rpmsenseFlags).
const (
	senseLess       = 1 << 1
	senseGreater    = 1 << 2
	senseEqual      = 1 << 3
	senseScriptPre  = 1 << 9
	senseScriptPost = 1 << 10
	sensePrereq     = 1 << 6
	sensePretrans   = 1 << 7
	sensePosttrans  = 1 << 5
	senseRpmlib     = 1 << 24
)

// File flags and modes.
const (
	fileGhost = 1 << 6
	modeDir   = 0o040000
	modeType  = 0o170000
)
