package docxmcp

// Bounds for untrusted input and generated output. These are not user-tunable:
// a model must not be able to raise them through tool arguments.
const (
	maxInputBytes      = 25 << 20
	maxZipEntries      = 1024
	maxEntryBytes      = 32 << 20
	maxTotalBytes      = 128 << 20
	maxCompressionRate = 200
	maxXMLBytes        = 32 << 20
	maxXMLDepth        = 128
	maxImageBytes      = 20 << 20
	maxImagePixels     = 25_000_000
	maxImages          = 64
	maxReadChars       = 2_000_000
	defaultReadChars   = 200_000
	maxOutputBytes     = 50 << 20
	maxBlocks          = 2000
	maxTableRows       = 200
	maxTableCols       = 50
	maxTableCells      = 20_000
	maxTextBytes       = 4 << 20
	maxReplacements    = 1000
)
