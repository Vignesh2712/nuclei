package dataformat

import (
	"strings"

	"github.com/clbanning/mxj/v2"
)

// XML is an XML encoder
type XML struct{}

// NewXML returns a new XML encoder
func NewXML() *XML {
	return &XML{}
}

// IsType returns true if the data is XML encoded
func (x *XML) IsType(data string) bool {
	return strings.HasPrefix(data, "<") && strings.HasSuffix(data, ">")
}

// Encode encodes the data into XML format
func (x *XML) Encode(data map[string]interface{}) ([]byte, error) {
	return mxj.Map(data).XmlIndent("", "  ")
}

// Decode decodes the data from XML format
func (x *XML) Decode(data []byte) (map[string]interface{}, error) {
	return mxj.NewMapXml(data)
}

// Name returns the name of the encoder
func (x *XML) Name() string {
	return "xml"
}