package parser

import (
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"path"

	"github.com/kistunium/sdk/pkg/kernel/config/normalize"
)

type XML struct {
	Path string
}

// Type Returns the file type "xml"
//
// This function returns a string indicating the type of file to handle.
//
// Parameters:
// - None
//
// Returns:
// - string: file type "xml"
func (x *XML) Type() string {
	return "xml"
}

// Load Loads and deserializes the XML file
//
// This function opens the XML file, deserializes it, and converts it into a map[string]string
// after normalization.
//
// Parameters:
// - None
//
// Returns:
// - map[string]string: normalized configuration map
// - error: error if any issues occurred during loading or deserialization
func (x *XML) Load() (map[string]string, error) {
	if ext := path.Ext(x.Path); ext != ".xml" {
		return nil, fmt.Errorf("invalid file extension: %s", ext)
	}

	var config map[string]string = make(map[string]string)

	file, err := os.Open(x.Path)
	if err != nil {
		return nil, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	if err := x.unmarshal(file, config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal XML: %w", err)
	}

	return config, nil
}

// unmarshal Deserializes the XML content into the provided output map
//
// This function reads the content from the provided file reader, processes the XML tokens,
// and fills the output map with the extracted values.
//
// Parameters:
// - file: io.Reader - the reader for the XML file content
// - output: map[string]any - the map to populate with the deserialized XML data
//
// Returns:
// - error: error if any issues occurred during deserialization
func (x *XML) unmarshal(file io.Reader, output map[string]string) error {
	decoder := xml.NewDecoder(file)
	n := makeNode("", nil)

	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}

		if err != nil {
			return err
		}

		switch token := token.(type) {
		case xml.StartElement:
			n = n.inNode(normalize.Key(token.Name.Local))
			for _, attr := range token.Attr {
				n = n.inNode(normalize.Key(attr.Name.Local))
				n.value = normalize.Value(attr.Value)
				n = n.outNode()
			}
		case xml.EndElement:
			n = n.outNode()
		case xml.CharData:
			n.value = normalize.Value(string(token))
		}
	}

	return explore(n, output)
}

// explore Recursively explores the node tree to populate the output map
//
// This function recursively traverses the node structure and populates the provided map with the node paths
// and their corresponding values.
//
// Parameters:
// - n: *node - the root node to explore
// - output: map[string]any - the map to populate with node values
//
// Returns:
// - error: error if any issues occurred during exploration
func explore(n *node, output map[string]string) error {
	if n.value != "" {
		output[n.getPath()] = n.value
	}

	if len(n.children) > 0 {
		for _, child := range n.children {
			if err := explore(child, output); err != nil {
				return err
			}
		}
	}

	return nil
}

// node represents a node in the XML structure
type node struct {
	value         string
	name          string
	children      []*node
	childrenNames map[string]int
	id            int
	parent        *node
}

// hasMultipleChildName Checks if the node has multiple children with the same name
//
// This function checks if the node has more than one child with the same name.
//
// Parameters:
// - None
//
// Returns:
// - bool: true if the node has multiple children with the same name, false otherwise
func (t *node) hasMultipleChildName() bool {
	count := 0
	for range t.childrenNames {
		if count > 1 {
			return true
		}
		count++
	}

	return false
}

// getPath Retrieves the full path of the node
//
// This function constructs and returns the full hierarchical path of the node, including
// its parent's path and its own identifier if necessary.
//
// Parameters:
// - None
//
// Returns:
// - string: the full path of the node
func (t *node) getPath() string {
	if t.parent == nil {
		return ""
	}

	if t.parent.childrenNames[t.name] > 1 {
		if t.hasMultipleChildName() {
			return fmt.Sprintf("%s.%d", t.parent.getPath(), t.id)
		}

		return fmt.Sprintf("%s.%s.%d", t.parent.getPath(), t.name, t.id)
	}

	if path := t.parent.getPath(); path != "" {
		return fmt.Sprintf("%s.%s", path, t.name)
	}

	return t.name
}

// inNode Adds a new child node
//
// This function creates a new child node with the given name and appends it to the list of children.
// It also updates the child name count for tracking multiple children with the same name.
//
// Parameters:
// - name: string - the name of the child node
//
// Returns:
// - *node: pointer to the newly created child node
func (t *node) inNode(name string) *node {
	if t.parent == nil && t.name == "" {
		t.name = name
		return t
	}

	n := makeNode(name, t)
	t.children = append(t.children, n)
	t.childrenNames = map[string]int{}
	for _, n := range t.children {
		t.childrenNames[n.name]++
		n.id = t.childrenNames[n.name] - 1
	}

	return n
}

// makeNode Creates a new node
//
// This function creates and returns a new node with the given name and parent.
//
// Parameters:
// - name: string - the name of the new node
// - parent: *node - the parent node of the new node
//
// Returns:
// - *node: pointer to the newly created node
func makeNode(name string, parent *node) *node {
	n := &node{
		parent:        parent,
		name:          name,
		childrenNames: map[string]int{},
		children:      []*node{},
	}

	return n
}

// outNode Moves back to the parent node
//
// This function moves the pointer from the current node back to its parent.
//
// Parameters:
// - None
//
// Returns:
// - *node: pointer to the parent node, or the current node if it has no parent
func (t *node) outNode() *node {
	if t.parent == nil {
		return t
	}

	return t.parent
}
