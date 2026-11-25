package docxmcp

import "fmt"

// Error is a stable, model-visible failure. Message never includes document
// text or other content, only the failed operation and its category.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func errf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Error codes are part of the tool contract. Keep them stable.
const (
	codeInvalidArguments = "INVALID_ARGUMENTS"
	codePathInvalid      = "PATH_INVALID"
	codePathOutside      = "PATH_OUTSIDE_WORKSPACE"
	codePathNotFound     = "PATH_NOT_FOUND"
	codePathNotRegular   = "PATH_NOT_REGULAR"
	codeSymlinkRejected  = "SYMLINK_REJECTED"
	codeUnsupportedExt   = "UNSUPPORTED_EXTENSION"
	codeFileTooLarge     = "FILE_TOO_LARGE"
	codeInvalidDocx      = "INVALID_DOCX"
	codeEncryptedDocx    = "ENCRYPTED_DOCX"
	codeMacroEnabled     = "MACRO_ENABLED"
	codeEmbeddedObject   = "EMBEDDED_OBJECT"
	codeZipLimit         = "ZIP_LIMIT"
	codeXMLLimit         = "XML_LIMIT"
	codeOutputExists     = "OUTPUT_EXISTS"
	codeOutputTooLarge   = "OUTPUT_TOO_LARGE"
	codeImageInvalid     = "IMAGE_INVALID"
	codeImageTooLarge    = "IMAGE_TOO_LARGE"
	codeImageLimit       = "IMAGE_LIMIT"
	codeReadFailed       = "DOCX_READ_FAILED"
	codeWriteFailed      = "DOCX_WRITE_FAILED"
)
