package memoryexpress

import "fmt"

// DocID returns the stable document ID for a Memory Express product.
func DocID(sku, storeCode string) string {
	return fmt.Sprintf("%s_%s", sku, storeCode)
}
