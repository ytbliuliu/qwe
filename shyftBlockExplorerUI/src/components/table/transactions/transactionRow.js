import React, { Component } from 'react';
import TransactionsTable from './transactionTable';
import classes from './table.css';
import axios from "axios/index";

class TransactionTable extends Component {
    constructor(props) {
        super(props);
        this.state = {
            data: []
        };
    }

    async componentDidMount() {
        try {
            const response = await axios.get(
                "http://localhost:8080/api/get_all_transactions")
            await this.setState({data: response.data});
        } catch (err) {
            console.log(err);
        }
    }

    render() {

        const table = this.state.data.map((data, i) => {
            const conversion = data.Cost / 10000000000000000000;
            return <TransactionsTable
                key={`${data.TxHash}${i}`}
                age={data.Age}
                txHash={data.TxHash}
                blockNumber={data.BlockNumber}
                to={data.ToGet}
                from={data.From}
                value={data.Amount}
                cost={conversion}
                getBlockTransactions={this.props.getBlockTransactions}
                detailTransactionHandler={this.props.detailTransactionHandler}
                detailAccountHandler={this.props.detailAccountHandler}
            />
        })

        let combinedClasses = ['responsive-table', classes.table];
        return (
            <table key={this.state.data.TxHash} className={combinedClasses.join(' ')}>
                <thead>
                    <tr>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}}>TxHash</th>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}}>Block</th>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}}>Age</th>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}}>From</th>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}}></th>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}}>To</th>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}}>Value</th>
                        <th scope="col" style={{fontSize: "8pt", backgroundColor: "white", color: "#4f2e7e"}} >TxFee</th>
                    </tr>
                </thead>
                {table}
            </table>
        );
    }
}
export default TransactionTable;
