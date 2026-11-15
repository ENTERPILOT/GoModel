package usage

import "go.mongodb.org/mongo-driver/v2/bson"

// mongoPromptCacheRawData projects raw_data down to promptCacheRawKeys; use it
// as the raw_data value in a projection. Missing fields are omitted.
func mongoPromptCacheRawData() bson.D {
	fields := make(bson.D, 0, len(promptCacheRawKeys))
	for _, key := range promptCacheRawKeys {
		fields = append(fields, bson.E{Key: key, Value: "$raw_data." + key})
	}
	return fields
}
