package server

import (
	"encoding/json"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/facette/facette/pkg/library"
	"github.com/facette/facette/pkg/utils"
	"github.com/facette/facette/thirdparty/github.com/fatih/set"
)

// CollectionResponse represents a collection response structure in the server backend.
type CollectionResponse struct {
	ItemResponse
	Parent      *string `json:"parent"`
	HasChildren bool    `json:"has_children"`
}

// CollectionListResponse represents a list of collections response structure in the backend server.
type CollectionListResponse []*CollectionResponse

func (r CollectionListResponse) Len() int {
	return len(r)
}

func (r CollectionListResponse) Less(i, j int) bool {
	return r[i].Name < r[j].Name
}

func (r CollectionListResponse) Swap(i, j int) {
	r[i], r[j] = r[j], r[i]
}

func (server *Server) handleCollection(writer http.ResponseWriter, request *http.Request) {
	type tmpCollection struct {
		*library.Collection
		Parent string `json:"parent"`
	}

	collectionID := strings.TrimPrefix(request.URL.Path, urlLibraryPath+"collections/")

	switch request.Method {
	case "DELETE":
		if collectionID == "" {
			server.handleResponse(writer, serverResponse{mesgMethodNotAllowed}, http.StatusMethodNotAllowed)
			return
		} else if !server.handleAuth(writer, request) {
			server.handleResponse(writer, serverResponse{mesgAuthenticationRequired}, http.StatusUnauthorized)
			return
		}

		err := server.Library.DeleteItem(collectionID, library.LibraryItemCollection)
		if os.IsNotExist(err) {
			server.handleResponse(writer, serverResponse{mesgResourceNotFound}, http.StatusNotFound)
			return
		} else if err != nil {
			log.Println("ERROR: " + err.Error())
			server.handleResponse(writer, serverResponse{mesgUnhandledError}, http.StatusInternalServerError)
			return
		}

		server.handleResponse(writer, nil, http.StatusOK)

		break

	case "GET", "HEAD":
		if collectionID == "" {
			server.handleCollectionList(writer, request)
			return
		}

		item, err := server.Library.GetItem(collectionID, library.LibraryItemCollection)
		if os.IsNotExist(err) {
			server.handleResponse(writer, serverResponse{mesgResourceNotFound}, http.StatusNotFound)
			return
		} else if err != nil {
			log.Println("ERROR: " + err.Error())
			server.handleResponse(writer, serverResponse{mesgUnhandledError}, http.StatusInternalServerError)
			return
		}

		server.handleResponse(writer, item, http.StatusOK)

		break

	case "POST", "PUT":
		if response, status := server.parseStoreRequest(writer, request, collectionID); status != http.StatusOK {
			server.handleResponse(writer, response, status)
			return
		}

		collectionTemp := &tmpCollection{
			Collection: &library.Collection{
				Item: library.Item{ID: collectionID},
			},
		}

		if request.Method == "POST" && request.FormValue("inherit") != "" {
			// Get collection from library
			item, err := server.Library.GetItem(request.FormValue("inherit"), library.LibraryItemCollection)
			if os.IsNotExist(err) {
				server.handleResponse(writer, serverResponse{mesgResourceNotFound}, http.StatusNotFound)
				return
			} else if err != nil {
				log.Println("ERROR: " + err.Error())
				server.handleResponse(writer, serverResponse{mesgUnhandledError}, http.StatusInternalServerError)
				return
			}

			*collectionTemp.Collection = *item.(*library.Collection)
			collectionTemp.Collection.ID = ""
			collectionTemp.Collection.Children = nil
		}

		collectionTemp.Collection.Modified = time.Now()

		// Parse input JSON for collection data
		body, _ := ioutil.ReadAll(request.Body)

		if err := json.Unmarshal(body, &collectionTemp); err != nil {
			log.Println("ERROR: " + err.Error())
			server.handleResponse(writer, serverResponse{mesgResourceInvalid}, http.StatusBadRequest)
			return
		}

		// Update parent relation
		if item, _ := server.Library.GetItem(collectionTemp.Parent, library.LibraryItemCollection); item != nil {
			collection := item.(*library.Collection)

			// Register parent relation
			collectionTemp.Collection.Parent = collection
			collectionTemp.Collection.ParentID = collectionTemp.Collection.Parent.ID
			collection.Children = append(collection.Children, collectionTemp.Collection)
		} else {
			// Remove existing parent relation
			if item, _ := server.Library.GetItem(collectionTemp.Collection.ID,
				library.LibraryItemCollection); item != nil {
				collection := item.(*library.Collection)

				if collection.Parent != nil {
					for index, child := range collection.Parent.Children {
						if reflect.DeepEqual(child, collection) {
							collection.Parent.Children = append(collection.Parent.Children[:index],
								collection.Parent.Children[index+1:]...)
							break
						}
					}
				}
			}
		}

		// Keep current children list
		if item, _ := server.Library.GetItem(collectionTemp.Collection.ID, library.LibraryItemCollection); item != nil {
			collectionTemp.Collection.Children = item.(*library.Collection).Children
		}

		// Store collection data
		err := server.Library.StoreItem(collectionTemp.Collection, library.LibraryItemCollection)
		if response, status := server.parseError(writer, request, err); status != http.StatusOK {
			log.Println("ERROR: " + err.Error())
			server.handleResponse(writer, response, status)
			return
		}

		if request.Method == "POST" {
			writer.Header().Add("Location", strings.TrimRight(request.URL.Path, "/")+"/"+collectionTemp.Collection.ID)
			server.handleResponse(writer, nil, http.StatusCreated)
		} else {
			server.handleResponse(writer, nil, http.StatusOK)
		}

		break

	default:
		server.handleResponse(writer, serverResponse{mesgMethodNotAllowed}, http.StatusMethodNotAllowed)
	}
}

func (server *Server) handleCollectionList(writer http.ResponseWriter, request *http.Request) {
	var offset, limit int

	if response, status := server.parseListRequest(writer, request, &offset, &limit); status != http.StatusOK {
		server.handleResponse(writer, response, status)
		return
	}

	// Check for item exclusion
	excludeSet := set.New()

	collectionStack := []*library.Collection{}

	if request.FormValue("exclude") != "" {
		if item, err := server.Library.GetItem(request.FormValue("exclude"), library.LibraryItemCollection); err == nil {
			collectionStack = append(collectionStack, item.(*library.Collection))
		}

		for len(collectionStack) > 0 {
			collection, collectionStack := collectionStack[0], collectionStack[1:]
			excludeSet.Add(collection.ID)
			collectionStack = append(collectionStack, collection.Children...)
		}
	}

	// Fill collections list
	response := make(CollectionListResponse, 0)

	for _, collection := range server.Library.Collections {
		if request.FormValue("parent") != "" && (request.FormValue("parent") == "" &&
			collection.Parent != nil || request.FormValue("parent") != "" &&
			(collection.Parent == nil || collection.Parent.ID != request.FormValue("parent"))) {
			continue
		}

		if request.FormValue("filter") != "" && !utils.FilterMatch(request.FormValue("filter"), collection.Name) {
			continue
		}

		// Skip excluded items
		if excludeSet.Has(collection.ID) {
			continue
		}

		collectionItem := &CollectionResponse{ItemResponse: ItemResponse{
			ID:          collection.ID,
			Name:        collection.Name,
			Description: collection.Description,
			Modified:    collection.Modified.Format(time.RFC3339),
		}, HasChildren: len(collection.Children) > 0}

		if collection.Parent != nil {
			collectionItem.Parent = &collection.Parent.ID
		}

		response = append(response, collectionItem)
	}

	server.applyCollectionListResponse(writer, request, response, offset, limit)

	server.handleResponse(writer, response, http.StatusOK)
}