package migrate

// All is the ordered list of upgrade steps. Add a new type and append it here.
var All = []Migration{
	CreateThumbnailMappings{},
	CreateSettings{},
	CreateVideoInfo{},
	DeleteCoverThumbnails{},
}
