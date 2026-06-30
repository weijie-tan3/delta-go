# Delta Go

[![License](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](https://opensource.org/licenses/Apache-2.0)

A native implementation of the [Delta](https://delta.io/) protocol in go.
This library started as a port of [delta-rs](https://github.com/delta-io/delta-rs/tree/main/rust).

This project is in alpha and the API is under development.

Current implementation is designed for highly concurrent writes; reads are not yet supported.

Our use case is to ingest data, write it to the Delta table folder as a Parquet file using a Parquet go library,
and then add the Parquet file to the Delta table using delta-go.


## Features

### Cloud Integrations

| Storage              |         Status        | Comment                                                          |
| -------------------- | :-------------------: | ---------------------------------------------------------------- |
| Local                |        ![done]        |                                                                  |
| S3 - AWS             |        ![done]        | Requires lock for concurrent writes                              |
| ADLS Gen2 - Azure    |        ![done]        | Hierarchical namespace (HNS) account; uses atomic rename         |


### Supported Operations

| Operation             |         Status        | Description                                 |
| --------------------- | :-------------------: | ------------------------------------------- |
| Create                |        ![done]        | Create a new table                          |
| Append                |        ![done]        | Append data in a Parquet file to a table    |
| Checkpoint            |        ![done]        | Create a V1 checkpoint for a table. Note that the optional log cleanup has not been fully tested.          |


### Protocol Support Level

| Writer Version | Requirement                                   |              Status               |
| -------------- | --------------------------------------------- | :-------------------------------: |
| Version 2      | Append Only Tables                            |              ![done]              |
| Version 2      | Column Invariants                             |                                   |
| Version 3      | Enforce `delta.checkpoint.writeStatsAsJson`   |                                   |
| Version 3      | Enforce `delta.checkpoint.writeStatsAsStruct` |                                   |
| Version 3      | CHECK constraints                             |                                   |
| Version 4      | Change Data Feed                              |                                   |
| Version 4      | Generated Columns                             |                                   |
| Version 5      | Column Mapping                                |                                   |
| Version 6      | Identity Columns                              |                                   |
| Version 7      | Table Features                                |                                   |

| Reader Version | Requirement                         | Status |
| -------------- | ----------------------------------- | ------ |
| Version 2      | Column Mapping                      |        |
| Version 3      | Table Features (requires reader V7) |        |

## Usage

Create a table in S3.  This table is configured to use DynamoDB LogStore locking to enable multi-cluster S3 support.
```golang
	store, err := s3store.New(s3Client, baseURI)
	logStore, err := dynamodblogstore.New(dynamodblogstore.Options{Client: dynamoDBClient, TableName: deltaLogStoreTableName})
	table := delta.NewTableWithLogStore(store, nillock.New(), logStore)
	metadata := delta.NewTableMetaData("Test Table", "test description", new(delta.Format).Default(), schema, []string{}, make(map[string]string))
	err := table.Create(*metadata, new(delta.Protocol).Default(), delta.CommitInfo{}, []delta.Add{})
```

Append data to the table.  The data is in a parquet file located at `parquetRelativePath`; the path is relative to the `baseURI`.
```golang
	add, _, err := delta.NewAdd(store, storage.NewPath(parquetRelativePath), make(map[string]string))
	transaction := table.CreateTransaction(delta.NewTransactionOptions())
	transaction.AddActions([]deltalib.Action{add})
	operation := delta.Write{Mode: delta.Append}
	appMetaData := make(map[string]any)
	appMetaData["isBlindAppend"] = true
	transaction.SetAppMetadata(appMetaData)
	transaction.SetOperation(operation)
	v, err := transaction.CommitLogStore()
```

There are also some simple examples available in the `examples/` folder.

## Azure (ADLS Gen2)

delta-go supports Azure Data Lake Storage Gen2 (storage accounts with hierarchical namespace enabled).
Unlike S3, ADLS Gen2 provides an atomic rename, so concurrent writers are coordinated by the object
store itself and no external LogStore is required for correctness.

The base URI accepts either the `az://` or `abfss://` scheme:

- `az://<container>/<path>` (the account is taken from the credential / `AZURE_STORAGE_ACCOUNT`)
- `abfss://<container>@<account>.dfs.core.windows.net/<path>`

Create a table on ADLS Gen2 using `azidentity.DefaultAzureCredential` (environment variables, managed
identity, or `az login`):
```golang
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	baseURI := storage.NewPath("abfss://my-container@myaccount.dfs.core.windows.net/tables/my-table")
	store, err := azurestore.NewWithCredential(baseURI, cred, nil)
	table := delta.NewTable(store, nillock.New(), localstate.New(-1))
	metadata := delta.NewTableMetaData("Test Table", "test description", new(delta.Format).Default(), schema, []string{}, make(map[string]string))
	err = table.Create(*metadata, new(delta.Protocol).Default(), delta.CommitInfo{}, []delta.Add{})
```

If you already have a configured `azdatalake` filesystem client (or a test mock), construct the store
directly with `azurestore.New(client, baseURI)`.

The data write then commit flow is the same as for any other store: write the Parquet file with
`store.Put`, then add it to the table with `delta.NewAdd` and commit a transaction.

Append on ADLS Gen2 uses the regular `Commit()` (no LogStore needed):
```golang
	add, _, err := delta.NewAdd(store, storage.NewPath(parquetRelativePath), make(map[string]string))
	transaction := table.CreateTransaction(delta.NewTransactionOptions())
	transaction.AddAction(add)
	transaction.SetOperation(delta.Write{Mode: delta.Append})
	v, err := transaction.Commit()
```

### Running the Azure integration tests

The live integration tests are gated behind the `azure_integration` build tag and are skipped unless
the target account is configured, so they never run as part of `go test ./...`. Copy
`.env-azure.template` to `.env-azure`, fill it in, then:
```sh
	set -a; source .env-azure; set +a
	go test -tags azure_integration ./storage/azurestore/ -run Integration -v
```
Authentication uses `azidentity.DefaultAzureCredential`. The account must have hierarchical namespace
(ADLS Gen2) enabled.

## Storage configuration on S3

If delta-go and other client(s) are being used to write to the same Delta table on S3, then it is important to configure all clients to use [multi-cluster LogStore](https://docs.delta.io/latest/delta-storage.html#-delta-storage-s3-multi-cluster) to avoid write conflicts.




[open]: https://cdn.jsdelivr.net/gh/Readme-Workflows/Readme-Icons@main/icons/octicons/IssueNeutral.svg
[done]: https://cdn.jsdelivr.net/gh/Readme-Workflows/Readme-Icons@main/icons/octicons/ApprovedChanges.svg
