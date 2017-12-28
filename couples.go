package hercules

import (
	"fmt"
	"io"
	"sort"

	"github.com/gogo/protobuf/proto"
	"gopkg.in/src-d/go-git.v4"
	"gopkg.in/src-d/go-git.v4/plumbing/object"
	"gopkg.in/src-d/go-git.v4/utils/merkletrie"
	"gopkg.in/src-d/hercules.v3/pb"
	"gopkg.in/src-d/hercules.v3/yaml"
)

type CouplesAnalysis struct {
	// The number of developers for which to build the matrix. 0 disables this analysis.
	PeopleNumber int

	// people store how many times every developer committed to every file.
	people []map[string]int
	// people_commits is the number of commits each author made
	people_commits []int
	// files store every file occurred in the same commit with every other file.
	files map[string]map[string]int
	// references IdentityDetector.ReversedPeopleDict
	reversedPeopleDict []string
}

type CouplesResult struct {
	PeopleMatrix []map[int]int64
	PeopleFiles  [][]int
	FilesMatrix  []map[int]int64
	Files        []string

	// references IdentityDetector.ReversedPeopleDict
	reversedPeopleDict []string
}

func (couples *CouplesAnalysis) Name() string {
	return "Couples"
}

func (couples *CouplesAnalysis) Provides() []string {
	return []string{}
}

func (couples *CouplesAnalysis) Requires() []string {
	arr := [...]string{"author", "changes"}
	return arr[:]
}

func (couples *CouplesAnalysis) ListConfigurationOptions() []ConfigurationOption {
	return []ConfigurationOption{}
}

func (couples *CouplesAnalysis) Configure(facts map[string]interface{}) {
	if val, exists := facts[FactIdentityDetectorPeopleCount].(int); exists {
		couples.PeopleNumber = val
		couples.reversedPeopleDict = facts[FactIdentityDetectorReversedPeopleDict].([]string)
	}
}

func (couples *CouplesAnalysis) Flag() string {
	return "couples"
}

func (couples *CouplesAnalysis) Initialize(repository *git.Repository) {
	couples.people = make([]map[string]int, couples.PeopleNumber+1)
	for i := range couples.people {
		couples.people[i] = map[string]int{}
	}
	couples.people_commits = make([]int, couples.PeopleNumber+1)
	couples.files = map[string]map[string]int{}
}

func (couples *CouplesAnalysis) Consume(deps map[string]interface{}) (map[string]interface{}, error) {
	author := deps["author"].(int)
	if author == MISSING_AUTHOR {
		author = couples.PeopleNumber
	}
	couples.people_commits[author] += 1
	tree_diff := deps["changes"].(object.Changes)
	context := make([]string, 0)
	deleteFile := func(name string) {
		// we do not remove the file from people - the context does not expire
		delete(couples.files, name)
		for _, otherFiles := range couples.files {
			delete(otherFiles, name)
		}
	}
	for _, change := range tree_diff {
		action, err := change.Action()
		if err != nil {
			return nil, err
		}
		toName := change.To.Name
		fromName := change.From.Name
		switch action {
		case merkletrie.Insert:
			context = append(context, toName)
			couples.people[author][toName] += 1
		case merkletrie.Delete:
			deleteFile(fromName)
			couples.people[author][fromName] += 1
		case merkletrie.Modify:
			if fromName != toName {
				// renamed
				couples.files[toName] = couples.files[fromName]
				for _, otherFiles := range couples.files {
					val, exists := otherFiles[fromName]
					if exists {
						otherFiles[toName] = val
					}
				}
				deleteFile(fromName)
				for _, authorFiles := range couples.people {
					val, exists := authorFiles[fromName]
					if exists {
						authorFiles[toName] = val
						delete(authorFiles, fromName)
					}
				}
			}
			context = append(context, toName)
			couples.people[author][toName] += 1
		}
	}
	for _, file := range context {
		for _, otherFile := range context {
			lane, exists := couples.files[file]
			if !exists {
				lane = map[string]int{}
				couples.files[file] = lane
			}
			lane[otherFile] += 1
		}
	}
	return nil, nil
}

func (couples *CouplesAnalysis) Finalize() interface{} {
	filesSequence := make([]string, len(couples.files))
	i := 0
	for file := range couples.files {
		filesSequence[i] = file
		i++
	}
	sort.Strings(filesSequence)
	filesIndex := map[string]int{}
	for i, file := range filesSequence {
		filesIndex[file] = i
	}

	peopleMatrix := make([]map[int]int64, couples.PeopleNumber+1)
	peopleFiles := make([][]int, couples.PeopleNumber+1)
	for i := range peopleMatrix {
		peopleMatrix[i] = map[int]int64{}
		for file, commits := range couples.people[i] {
			fi, exists := filesIndex[file]
			if exists {
				peopleFiles[i] = append(peopleFiles[i], fi)
			}
			for j, otherFiles := range couples.people {
				otherCommits := otherFiles[file]
				delta := otherCommits
				if otherCommits > commits {
					delta = commits
				}
				if delta > 0 {
					peopleMatrix[i][j] += int64(delta)
				}
			}
		}
		sort.Ints(peopleFiles[i])
	}

	filesMatrix := make([]map[int]int64, len(filesIndex))
	for i := range filesMatrix {
		filesMatrix[i] = map[int]int64{}
		for otherFile, cooccs := range couples.files[filesSequence[i]] {
			filesMatrix[i][filesIndex[otherFile]] = int64(cooccs)
		}
	}
	return CouplesResult{
		PeopleMatrix:       peopleMatrix,
		PeopleFiles:        peopleFiles,
		Files:              filesSequence,
		FilesMatrix:        filesMatrix,
		reversedPeopleDict: couples.reversedPeopleDict,
	}
}

func (couples *CouplesAnalysis) Serialize(result interface{}, binary bool, writer io.Writer) error {
	couplesResult := result.(CouplesResult)
	if binary {
		return couples.serializeBinary(&couplesResult, writer)
	}
	couples.serializeText(&couplesResult, writer)
	return nil
}

func (couples *CouplesAnalysis) Deserialize(pbmessage []byte) (interface{}, error) {
	message := pb.CouplesAnalysisResults{}
	err := proto.Unmarshal(pbmessage, &message)
	if err != nil {
		return nil, err
	}
	result := CouplesResult{
		Files:              message.FileCouples.Index,
		FilesMatrix:        make([]map[int]int64, message.FileCouples.Matrix.NumberOfRows),
		PeopleFiles:        make([][]int, len(message.PeopleCouples.Index)),
		PeopleMatrix:       make([]map[int]int64, message.PeopleCouples.Matrix.NumberOfRows),
		reversedPeopleDict: message.PeopleCouples.Index,
	}
	for i, files := range message.PeopleFiles {
		result.PeopleFiles[i] = make([]int, len(files.Files))
		for j, val := range files.Files {
			result.PeopleFiles[i][j] = int(val)
		}
	}
	convertCSR := func(dest []map[int]int64, src *pb.CompressedSparseRowMatrix) {
		for indptr := range src.Indptr {
			if indptr == 0 {
				continue
			}
			dest[indptr-1] = map[int]int64{}
			for j := src.Indptr[indptr-1]; j < src.Indptr[indptr]; j++ {
				dest[indptr-1][int(src.Indices[j])] = src.Data[j]
			}
		}
	}
	convertCSR(result.FilesMatrix, message.FileCouples.Matrix)
	convertCSR(result.PeopleMatrix, message.PeopleCouples.Matrix)
	return result, nil
}

func (couples *CouplesAnalysis) MergeResults(r1, r2 interface{}, c1, c2 *CommonAnalysisResult) interface{} {
	cr1 := r1.(CouplesResult)
	cr2 := r2.(CouplesResult)
	merged := CouplesResult{}
	var people, files map[string][3]int
	people, merged.reversedPeopleDict = IdentityDetector{}.MergeReversedDicts(
		cr1.reversedPeopleDict, cr2.reversedPeopleDict)
	files, merged.Files = IdentityDetector{}.MergeReversedDicts(cr1.Files, cr2.Files)
	merged.PeopleFiles = make([][]int, len(merged.reversedPeopleDict))
	peopleFilesDicts := make([]map[int]bool, len(merged.reversedPeopleDict))
	addPeopleFiles := func(peopleFiles [][]int, reversedPeopleDict []string,
		reversedFilesDict []string) {
		for pi, fs := range peopleFiles {
			idx := people[reversedPeopleDict[pi]][0]
			m := peopleFilesDicts[idx]
			if m == nil {
				m = map[int]bool{}
				peopleFilesDicts[idx] = m
			}
			for _, f := range fs {
				m[files[reversedFilesDict[f]][0]] = true
			}
		}
	}
	addPeopleFiles(cr1.PeopleFiles, cr1.reversedPeopleDict, cr1.Files)
	addPeopleFiles(cr2.PeopleFiles, cr2.reversedPeopleDict, cr2.Files)
	for i, m := range peopleFilesDicts {
		merged.PeopleFiles[i] = make([]int, len(m))
		j := 0
		for f := range m {
			merged.PeopleFiles[i][j] = f
			j++
		}
		sort.Ints(merged.PeopleFiles[i])
	}
	merged.PeopleMatrix = make([]map[int]int64, len(merged.reversedPeopleDict)+1)
	addPeople := func(peopleMatrix []map[int]int64, reversedPeopleDict []string,
		reversedFilesDict []string) {
		for pi, pc := range peopleMatrix {
			var idx int
			if pi < len(reversedPeopleDict) {
				idx = people[reversedPeopleDict[pi]][0]
			} else {
				idx = len(merged.reversedPeopleDict)
			}
			m := merged.PeopleMatrix[idx]
			if m == nil {
				m = map[int]int64{}
				merged.PeopleMatrix[idx] = m
			}
			for file, val := range pc {
				m[files[reversedFilesDict[file]][0]] += val
			}
		}
	}
	addPeople(cr1.PeopleMatrix, cr1.reversedPeopleDict, cr1.Files)
	addPeople(cr2.PeopleMatrix, cr2.reversedPeopleDict, cr2.Files)
	merged.FilesMatrix = make([]map[int]int64, len(merged.Files))
	addFiles := func(filesMatrix []map[int]int64, reversedFilesDict []string) {
		for fi, fc := range filesMatrix {
			idx := people[reversedFilesDict[fi]][0]
			m := merged.FilesMatrix[idx]
			if m == nil {
				m = map[int]int64{}
				merged.FilesMatrix[idx] = m
			}
			for file, val := range fc {
				m[files[reversedFilesDict[file]][0]] += val
			}
		}
	}
	addFiles(cr1.FilesMatrix, cr1.Files)
	addFiles(cr2.FilesMatrix, cr2.Files)
	return merged
}

func (couples *CouplesAnalysis) serializeText(result *CouplesResult, writer io.Writer) {
	fmt.Fprintln(writer, "  files_coocc:")
	fmt.Fprintln(writer, "    index:")
	for _, file := range result.Files {
		fmt.Fprintf(writer, "      - %s\n", yaml.SafeString(file))
	}

	fmt.Fprintln(writer, "    matrix:")
	for _, files := range result.FilesMatrix {
		fmt.Fprint(writer, "      - {")
		indices := []int{}
		for file := range files {
			indices = append(indices, file)
		}
		sort.Ints(indices)
		for i, file := range indices {
			fmt.Fprintf(writer, "%d: %d", file, files[file])
			if i < len(indices)-1 {
				fmt.Fprint(writer, ", ")
			}
		}
		fmt.Fprintln(writer, "}")
	}

	fmt.Fprintln(writer, "  people_coocc:")
	fmt.Fprintln(writer, "    index:")
	for _, person := range couples.reversedPeopleDict {
		fmt.Fprintf(writer, "      - %s\n", yaml.SafeString(person))
	}

	fmt.Fprintln(writer, "    matrix:")
	for _, people := range result.PeopleMatrix {
		fmt.Fprint(writer, "      - {")
		indices := []int{}
		for file := range people {
			indices = append(indices, file)
		}
		sort.Ints(indices)
		for i, person := range indices {
			fmt.Fprintf(writer, "%d: %d", person, people[person])
			if i < len(indices)-1 {
				fmt.Fprint(writer, ", ")
			}
		}
		fmt.Fprintln(writer, "}")
	}

	fmt.Fprintln(writer, "    author_files:") // sorted by number of files each author changed
	peopleFiles := sortByNumberOfFiles(result.PeopleFiles, couples.reversedPeopleDict, result.Files)
	for _, authorFiles := range peopleFiles {
		fmt.Fprintf(writer, "      - %s:\n", yaml.SafeString(authorFiles.Author))
		sort.Strings(authorFiles.Files)
		for _, file := range authorFiles.Files {
			fmt.Fprintf(writer, "        - %s\n", yaml.SafeString(file)) // sorted by path
		}
	}
}

func sortByNumberOfFiles(
	peopleFiles [][]int, peopleDict []string, filesDict []string) authorFilesList {
	var pfl authorFilesList
	for peopleIdx, files := range peopleFiles {
		if peopleIdx < len(peopleDict) {
			fileNames := make([]string, len(files))
			for i, fi := range files {
				fileNames[i] = filesDict[fi]
			}
			pfl = append(pfl, authorFiles{peopleDict[peopleIdx], fileNames})
		}
	}
	sort.Sort(pfl)
	return pfl
}

type authorFiles struct {
	Author string
	Files  []string
}

type authorFilesList []authorFiles

func (s authorFilesList) Len() int {
	return len(s)
}
func (s authorFilesList) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s authorFilesList) Less(i, j int) bool {
	return len(s[i].Files) < len(s[j].Files)
}

func (couples *CouplesAnalysis) serializeBinary(result *CouplesResult, writer io.Writer) error {
	message := pb.CouplesAnalysisResults{}

	message.FileCouples = &pb.Couples{
		Index:  result.Files,
		Matrix: pb.MapToCompressedSparseRowMatrix(result.FilesMatrix),
	}
	message.PeopleCouples = &pb.Couples{
		Index:  result.reversedPeopleDict,
		Matrix: pb.MapToCompressedSparseRowMatrix(result.PeopleMatrix),
	}
	message.PeopleFiles = make([]*pb.TouchedFiles, len(result.reversedPeopleDict))
	for key := range result.reversedPeopleDict {
		files := result.PeopleFiles[key]
		int32Files := make([]int32, len(files))
		for i, f := range files {
			int32Files[i] = int32(f)
		}
		message.PeopleFiles[key] = &pb.TouchedFiles{
			Files: int32Files,
		}
	}

	serialized, err := proto.Marshal(&message)
	if err != nil {
		return err
	}
	writer.Write(serialized)
	return nil
}

func init() {
	Registry.Register(&CouplesAnalysis{})
}
